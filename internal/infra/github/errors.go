// Package github holds the GitHub adapter for Beacon.
//
// This file normalizes GitHub error responses into the shared domain.AppError
// shape. Three sources produce RATE_LIMITED: GraphQL 200-with-errors with
// x-ratelimit-remaining:0, REST 403/429 with an explicit retry signal, and the
// secondary limit carried by retry-after. x-ratelimit-reset is the hourly
// window reset GitHub sends on every response. It applies only with
// x-ratelimit-remaining:0. Server timeouts (502/504) are a retry decision with
// backoff, not an AppError. Header values are authoritative over body hints.
//
// The second half of the file is the cross-platform CI status adapter
// (ADR 0039 §3, T3.62). Three providers expose different payloads; each
// adapter keeps its format inside Parse and returns one provider-neutral
// StatusResult or a structured domain.AppError. Nothing there evaluates a
// verdict: StatusResult only prepares the Rollup, Failures and Ship facts that
// the domain verdict engine already reads (T2.15, ADR 0040 §2).
package github

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	baseBackoff = 100 * time.Millisecond
	maxBackoff  = 5 * time.Second
	maxJitter   = time.Second
)

// Result is the normalized classification of a GitHub response.
//
// Exactly one side is set: Err for a RATE_LIMITED response the caller must
// surface, Conflict for a merge conflict returned as structured data (ADR 0033
// §2), or Retry/Backoff for a 502/504 the caller retries with backoff. A
// healthy response leaves every field unset.
type Result struct {
	Err      *domain.AppError
	Conflict *domain.ConflictResult
	Retry    bool
	Backoff  time.Duration
}

// Normalizer classifies GitHub responses. Jitter spreads retries so a fleet
// of clients does not hit GitHub again in lockstep. The clock and jitter are
// fields so tests can fix them and assert exact retryAfterMs values.
type Normalizer struct {
	now    func() time.Time
	jitter func() time.Duration
}

// NewNormalizer returns a Normalizer backed by the wall clock and random jitter.
func NewNormalizer() *Normalizer {
	return &Normalizer{now: time.Now, jitter: defaultJitter}
}

// Normalize inspects a GitHub response and returns how the caller should
// handle it. Header values win over body hints for the retry delay.
func (n *Normalizer) Normalize(status int, header http.Header, body []byte) Result {
	switch {
	case status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		return Result{Retry: true, Backoff: n.Backoff(1)}
	case status == http.StatusTooManyRequests:
		return n.rateLimited("GitHub REST 429", header, body)
	case conflictStatus(status):
		if c, ok := ParseConflict(status, body); ok {
			return Result{Conflict: c}
		}
		return Result{}
	case status == http.StatusForbidden:
		if rateLimitSignaled(header, body) {
			return n.rateLimited("GitHub REST 403", header, body)
		}
		return Result{}
	case status == http.StatusOK:
		if hasGraphQLErrors(body) && remainingIsZero(header) {
			return n.rateLimited("GitHub GraphQL rate limit", header, body)
		}
		if n.delay(header, body) > 0 {
			return n.rateLimited("GitHub secondary rate limit", header, body)
		}
		return Result{}
	default:
		return Result{}
	}
}

// conflictPayload is the part of a GitHub error body that describes a
// conflict. GitHub does not keep conflicting paths in one fixed field, so
// files, paths and per-error path/field are all read.
type conflictPayload struct {
	Message string   `json:"message"`
	Type    string   `json:"type"`
	Files   []string `json:"files"`
	Paths   []string `json:"paths"`
	Errors  []struct {
		Message  string `json:"message"`
		Code     string `json:"code"`
		Field    string `json:"field"`
		Path     string `json:"path"`
		Resource string `json:"resource"`
	} `json:"errors"`
}

// conflictStatus reports whether status is one GitHub uses for a failed merge.
func conflictStatus(status int) bool {
	return status == http.StatusConflict ||
		status == http.StatusUnprocessableEntity ||
		status == http.StatusMethodNotAllowed
}

// ParseConflict maps a GitHub response to a domain.ConflictResult. It returns
// ok=false when the body does not describe a conflict, so a plain 409 or 422
// can still be handled by the caller. Paths are read from structured fields,
// never from the message text (ADR 0033 §2).
func ParseConflict(status int, body []byte) (*domain.ConflictResult, bool) {
	var p conflictPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, false
	}

	paths := append([]string(nil), p.Files...)
	paths = append(paths, p.Paths...)
	hasSignal := conflictText(p.Message)
	for _, e := range p.Errors {
		if conflictText(e.Message) || conflictText(e.Code) || conflictText(e.Resource) {
			hasSignal = true
		}
		switch {
		case e.Path != "":
			paths = append(paths, e.Path)
		case strings.Contains(e.Field, "/"):
			paths = append(paths, e.Field)
		}
	}
	if len(paths) > 0 && conflictStatus(status) {
		hasSignal = true
	}
	if !hasSignal {
		return nil, false
	}

	typ := domain.ConflictTypeMerge
	switch p.Type {
	case string(domain.ConflictTypeMerge), string(domain.ConflictTypeFileOwnership):
		typ = domain.ConflictType(p.Type)
	}
	c := domain.NewConflictResult(typ, paths, p.Message)
	return &c, true
}

// conflictText reports whether s names a conflict in GitHub's wording.
func conflictText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "conflict") || strings.Contains(s, "not mergeable")
}

// Backoff returns the delay before the next attempt of a 502/504 response.
// attempt is 1-based; the delay doubles until maxBackoff, then jitter is added.
func (n *Normalizer) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := baseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			d = maxBackoff
			break
		}
	}
	return d + n.jitterOr()
}

func (n *Normalizer) rateLimited(message string, header http.Header, body []byte) Result {
	err := domain.NewAppError(domain.CodeRateLimited, message)
	if ms := n.retryAfterMs(n.delay(header, body)); ms != nil {
		err.RetryAfterMs = ms
	}
	return Result{Err: err}
}

// retryAfterMs converts a base delay into the MCP retryAfterMs field, adding
// jitter. A non-positive base yields no field.
func (n *Normalizer) retryAfterMs(base time.Duration) *int64 {
	if base <= 0 {
		return nil
	}
	ms := base.Milliseconds() + n.jitterOr().Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}

// delay returns the wait before retrying. Explicit retry signals take priority
// over body hints, matching ADR 0013. A bare x-ratelimit-reset is the window
// reset GitHub sends on every response, not a rate-limit signal.
func (n *Normalizer) delay(header http.Header, body []byte) time.Duration {
	if d := headerDelay(header, n.nowOr()()); d > 0 {
		return d
	}
	return bodyDelay(body)
}

// rateLimitSignaled reports whether a 403 carries a real rate-limit signal.
// x-ratelimit-reset is present on every GitHub response, so it never signals a
// limit on its own; a plain 403 stays unclassified without retry-after,
// remaining:0 or an explicit body hint.
func rateLimitSignaled(header http.Header, body []byte) bool {
	return headerValue(header, "retry-after") != "" ||
		remainingIsZero(header) ||
		bodyDelay(body) > 0
}

func remainingIsZero(header http.Header) bool {
	return headerValue(header, "x-ratelimit-remaining") == "0"
}

func hasGraphQLErrors(body []byte) bool {
	var doc struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	return len(doc.Errors) > 0
}

// headerDelay reads the wait from response headers. retry-after is an explicit
// secondary-limit signal. x-ratelimit-reset is only the end of the current
// window, so it is used only when x-ratelimit-remaining is 0.
func headerDelay(h http.Header, now time.Time) time.Duration {
	if v := headerValue(h, "retry-after"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	if v := headerValue(h, "x-ratelimit-reset"); v != "" && remainingIsZero(h) {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Unix(sec, 0).Sub(now); d > 0 {
				return d
			}
		}
	}
	return 0
}

// bodyDelay reads only explicit retry hints from the body. A bare reset is the
// window reset time and carries no sign of exhaustion, so it is ignored here.
// headerDelay handles reset once x-ratelimit-remaining is 0.
func bodyDelay(body []byte) time.Duration {
	var doc struct {
		RetryAfter   *float64 `json:"retry_after"`
		RetryAfterMs *int64   `json:"retryAfterMs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0
	}
	if doc.RetryAfter != nil && *doc.RetryAfter > 0 {
		return time.Duration(*doc.RetryAfter * float64(time.Second))
	}
	if doc.RetryAfterMs != nil && *doc.RetryAfterMs > 0 {
		return time.Duration(*doc.RetryAfterMs) * time.Millisecond
	}
	return 0
}

func (n *Normalizer) nowOr() func() time.Time {
	if n.now == nil {
		return time.Now
	}
	return n.now
}

func (n *Normalizer) jitterOr() time.Duration {
	if n.jitter == nil {
		return defaultJitter()
	}
	return n.jitter()
}

func defaultJitter() time.Duration {
	return time.Duration(rand.Int64N(int64(maxJitter) + 1))
}

// --- Cross-platform CI status adapter (ADR 0039 §3, T3.62) ---

// Provider identifies a CI platform whose status payload an adapter parses.
type Provider string

const (
	ProviderGitHub    Provider = "github"
	ProviderGitLab    Provider = "gitlab"
	ProviderBitbucket Provider = "bitbucket"
)

// CheckState is the provider-neutral state of one check. The values match the
// rollup states the verdict engine reads, so the adapter only prepares input.
type CheckState string

const (
	CheckSuccess CheckState = "SUCCESS"
	CheckFailure CheckState = "FAILURE"
	CheckError   CheckState = "ERROR"
	CheckPending CheckState = "PENDING"
	CheckSkipped CheckState = "SKIPPED"
	CheckNeutral CheckState = "NEUTRAL"
)

// Check is one CI check after provider fields have been normalized.
type Check struct {
	Name       string     `json:"name"`
	State      CheckState `json:"state"`
	DetailsURL string     `json:"detailsUrl,omitempty"`
}

// StatusResult is the provider-neutral observation of a CI status. Rollup,
// Failures and Ship are the fields the verdict engine reads from
// domain.Snapshot; Apply copies them without computing or changing the verdict.
type StatusResult struct {
	Rollup   string   `json:"rollup"`
	Checks   []Check  `json:"checks"`
	Failures []string `json:"failures"`
}

// Apply stores the parsed observation in s. It fills the status-derived fields,
// including the ship facts the ready gate reads (ADR 0040 §2), and leaves the
// verdict evaluation to the domain package.
func (r StatusResult) Apply(s *domain.Snapshot) {
	s.Rollup = r.Rollup
	s.Failures = append([]string(nil), r.Failures...)
	s.Ship = shipFactsFromChecks(r.Checks)
}

// shipFactsFromChecks derives the ship facts from the observed CI checks
// (ADR 0040 §2, T3.78). It mirrors mcp.shipFactsFromWatch: a fact turns on only
// for a green check whose name carries the matching role. A failing or missing
// check leaves its fact false, and Trivial stays false because a status payload
// carries no explicit triviality signal.
func shipFactsFromChecks(checks []Check) domain.ShipFacts {
	var facts domain.ShipFacts
	for _, c := range checks {
		if c.State != CheckSuccess {
			continue
		}
		name := strings.ToLower(c.Name)
		if strings.Contains(name, "build") {
			facts.Build = true
		}
		if strings.Contains(name, "audit") {
			facts.Audit = true
		}
		if strings.Contains(name, "test") {
			facts.TDD = true
		}
	}
	return facts
}

// Structured error codes for status parsing. They travel as domain.AppError
// codes so failures reach the unified MCP error shape (ADR 0009).
const (
	CodeCIUnknownProvider domain.ErrorCode = "CI_UNKNOWN_PROVIDER"
	CodeCIMalformed       domain.ErrorCode = "CI_MALFORMED"
	CodeCIMissingStatus   domain.ErrorCode = "CI_MISSING_STATUS"
	CodeCIInvalidStatus   domain.ErrorCode = "CI_INVALID_STATUS"
)

func ciAppError(code domain.ErrorCode, message string) *domain.AppError {
	return &domain.AppError{Code: code, Message: message, Status: http.StatusBadRequest}
}

func malformedStatus(provider Provider, err error) *domain.AppError {
	return ciAppError(CodeCIMalformed,
		fmt.Sprintf("%s: malformed status payload: %v", provider, err))
}

func missingStatus(provider Provider) *domain.AppError {
	return ciAppError(CodeCIMissingStatus,
		fmt.Sprintf("%s: status payload has no checks or status", provider))
}

func invalidStatus(provider Provider, value string) *domain.AppError {
	return ciAppError(CodeCIInvalidStatus,
		fmt.Sprintf("%s: unrecognized status %q", provider, value))
}

// StatusAdapter is the single interface every CI provider implements. All
// provider-specific field names and status vocabularies live inside Parse.
type StatusAdapter interface {
	Provider() Provider
	Parse(body []byte) (StatusResult, *domain.AppError)
}

// StatusParser is the one entry point for status parsing. It dispatches to a
// provider adapter and turns an unknown provider into a structured error.
type StatusParser struct {
	adapters map[Provider]StatusAdapter
}

// NewStatusParser returns a parser with the GitHub, GitLab and Bitbucket
// adapters registered.
func NewStatusParser() *StatusParser {
	p := &StatusParser{adapters: make(map[Provider]StatusAdapter)}
	p.Register(NewGitHubActionsAdapter())
	p.Register(NewGitLabAdapter())
	p.Register(NewBitbucketAdapter())
	return p
}

// Register adds or replaces an adapter for its provider.
func (p *StatusParser) Register(a StatusAdapter) {
	if p.adapters == nil {
		p.adapters = make(map[Provider]StatusAdapter)
	}
	p.adapters[a.Provider()] = a
}

// Parse normalizes a raw status payload from provider into one StatusResult.
func (p *StatusParser) Parse(provider Provider, body []byte) (StatusResult, *domain.AppError) {
	a, ok := p.adapters[provider]
	if !ok {
		return StatusResult{}, ciAppError(CodeCIUnknownProvider,
			fmt.Sprintf("unknown CI provider %q", provider))
	}
	return a.Parse(body)
}

// buildRollup derives the neutral rollup and the failing-check names from
// checks. Skipped and neutral checks never appear in Failures, matching the
// verdict engine's Snapshot contract.
func buildRollup(checks []Check) (string, []string) {
	var failures []string
	hasFailure, hasError, hasPending := false, false, false
	for _, c := range checks {
		switch c.State {
		case CheckFailure:
			hasFailure = true
			failures = append(failures, c.Name)
		case CheckError:
			hasError = true
			failures = append(failures, c.Name)
		case CheckPending:
			hasPending = true
		}
	}
	switch {
	case hasFailure:
		return "FAILURE", failures
	case hasError:
		return "ERROR", failures
	case hasPending:
		return "PENDING", failures
	default:
		return "SUCCESS", failures
	}
}

// --- GitHub Actions ---

// GitHubActionsAdapter parses a GitHub check-runs response. Runs are reduced
// to the current attempt of each named check before the rollup is built
// (ADR 0048 §1).
type GitHubActionsAdapter struct{}

var _ StatusAdapter = GitHubActionsAdapter{}

func NewGitHubActionsAdapter() StatusAdapter { return GitHubActionsAdapter{} }

func (GitHubActionsAdapter) Provider() Provider { return ProviderGitHub }

type ghCheckRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	HTMLURL     string `json:"html_url"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

type ghCheckRuns struct {
	CheckRuns []ghCheckRun `json:"check_runs"`
}

// currentRuns reduces a raw check-runs list to the latest attempt of each
// named check. A re-run reuses the check name and creates a fresh run with a
// higher id and later timestamps, so the green re-run wins over an old red
// result in the raw statusCheckRollup. The reduction lives in the adapter and
// leaves the domain decision table, ADR 0004 batch order and response schema
// untouched (ADR 0048 §1). Check names keep their order of first appearance,
// which makes the result independent of how the provider ordered the runs.
func currentRuns(runs []ghCheckRun) []ghCheckRun {
	latest := make(map[string]int, len(runs))
	order := make([]string, 0, len(runs))
	for i, r := range runs {
		j, seen := latest[r.Name]
		if !seen {
			latest[r.Name] = i
			order = append(order, r.Name)
			continue
		}
		if newerRun(r, runs[j]) {
			latest[r.Name] = i
		}
	}
	out := make([]ghCheckRun, 0, len(order))
	for _, name := range order {
		out = append(out, runs[latest[name]])
	}
	return out
}

// newerRun reports whether a is a later attempt than b. The most precise
// available timestamp decides; id breaks ties and covers runs without a
// timestamp.
func newerRun(a, b ghCheckRun) bool {
	ta, tb := runTime(a), runTime(b)
	if !ta.Equal(tb) {
		return ta.After(tb)
	}
	return a.ID > b.ID
}

// runTime returns the best timestamp a run carries, or the zero time when no
// timestamp field parses. RFC3339 is GitHub's wire format.
func runTime(r ghCheckRun) time.Time {
	for _, s := range []string{r.StartedAt, r.CreatedAt, r.CompletedAt} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (a GitHubActionsAdapter) Parse(body []byte) (StatusResult, *domain.AppError) {
	var doc ghCheckRuns
	if err := json.Unmarshal(body, &doc); err != nil {
		return StatusResult{}, malformedStatus(a.Provider(), err)
	}
	if doc.CheckRuns == nil {
		return StatusResult{}, missingStatus(a.Provider())
	}
	runs := currentRuns(doc.CheckRuns)
	checks := make([]Check, 0, len(runs))
	for _, run := range runs {
		state, ok := githubCheckState(run.Status, run.Conclusion)
		if !ok {
			return StatusResult{}, invalidStatus(a.Provider(), run.Status+"/"+run.Conclusion)
		}
		checks = append(checks, Check{Name: run.Name, State: state, DetailsURL: run.HTMLURL})
	}
	roll, failures := buildRollup(checks)
	return StatusResult{Rollup: roll, Checks: checks, Failures: failures}, nil
}

var githubPendingStates = map[string]CheckState{
	"queued": CheckPending, "in_progress": CheckPending,
	"requested": CheckPending, "waiting": CheckPending,
}

var githubConclusionStates = map[string]CheckState{
	"success": CheckSuccess, "failure": CheckFailure,
	"timed_out": CheckFailure, "startup_failure": CheckFailure,
	"skipped": CheckSkipped, "neutral": CheckNeutral,
	"cancelled": CheckNeutral, "canceled": CheckNeutral,
	"action_required": CheckNeutral, "stale": CheckNeutral,
}

func githubCheckState(status, conclusion string) (CheckState, bool) {
	if status != "completed" {
		state, ok := githubPendingStates[status]
		return state, ok
	}
	state, ok := githubConclusionStates[conclusion]
	return state, ok
}

// --- GitLab CI ---

// GitLabAdapter parses a GitLab pipeline response with its jobs.
type GitLabAdapter struct{}

var _ StatusAdapter = GitLabAdapter{}

func NewGitLabAdapter() StatusAdapter { return GitLabAdapter{} }

func (GitLabAdapter) Provider() Provider { return ProviderGitLab }

type glJob struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
	WebURL       string `json:"web_url"`
}

type glPipeline struct {
	Status string  `json:"status"`
	Jobs   []glJob `json:"jobs"`
}

func (a GitLabAdapter) Parse(body []byte) (StatusResult, *domain.AppError) {
	var doc glPipeline
	if err := json.Unmarshal(body, &doc); err != nil {
		return StatusResult{}, malformedStatus(a.Provider(), err)
	}
	if doc.Status == "" || doc.Jobs == nil {
		return StatusResult{}, missingStatus(a.Provider())
	}
	if _, ok := gitlabCheckState(doc.Status, false); !ok {
		return StatusResult{}, invalidStatus(a.Provider(), doc.Status)
	}
	checks := make([]Check, 0, len(doc.Jobs))
	for _, job := range doc.Jobs {
		state, ok := gitlabCheckState(job.Status, job.AllowFailure)
		if !ok {
			return StatusResult{}, invalidStatus(a.Provider(), job.Status)
		}
		checks = append(checks, Check{Name: job.Name, State: state, DetailsURL: job.WebURL})
	}
	roll, failures := buildRollup(checks)
	return StatusResult{Rollup: roll, Checks: checks, Failures: failures}, nil
}

var gitlabCheckStates = map[string]CheckState{
	"success": CheckSuccess, "failed": CheckFailure,
	"canceled": CheckNeutral, "skipped": CheckSkipped,
	"created": CheckPending, "preparing": CheckPending,
	"pending": CheckPending, "running": CheckPending,
	"waiting_for_resource": CheckPending, "scheduled": CheckPending,
	"manual": CheckPending,
}

func gitlabCheckState(status string, allowFailure bool) (CheckState, bool) {
	state, ok := gitlabCheckStates[status]
	if !ok {
		return "", false
	}
	if status == "failed" && allowFailure {
		return CheckNeutral, true
	}
	return state, true
}

// --- Bitbucket Pipelines ---

// BitbucketAdapter parses a Bitbucket build-status response.
type BitbucketAdapter struct{}

var _ StatusAdapter = BitbucketAdapter{}

func NewBitbucketAdapter() StatusAdapter { return BitbucketAdapter{} }

func (BitbucketAdapter) Provider() Provider { return ProviderBitbucket }

type bbStatus struct {
	State string `json:"state"`
	Name  string `json:"name"`
	Key   string `json:"key"`
	URL   string `json:"url"`
}

type bbStatuses struct {
	Values []bbStatus `json:"values"`
}

func (a BitbucketAdapter) Parse(body []byte) (StatusResult, *domain.AppError) {
	var doc bbStatuses
	if err := json.Unmarshal(body, &doc); err != nil {
		return StatusResult{}, malformedStatus(a.Provider(), err)
	}
	items := doc.Values
	if items == nil {
		// A single build-status payload has no values wrapper.
		var one bbStatus
		if err := json.Unmarshal(body, &one); err != nil {
			return StatusResult{}, malformedStatus(a.Provider(), err)
		}
		if one.State == "" {
			return StatusResult{}, missingStatus(a.Provider())
		}
		items = []bbStatus{one}
	}
	checks := make([]Check, 0, len(items))
	for _, item := range items {
		state, ok := bitbucketCheckState(item.State)
		if !ok {
			return StatusResult{}, invalidStatus(a.Provider(), item.State)
		}
		name := item.Name
		if name == "" {
			name = item.Key
		}
		checks = append(checks, Check{Name: name, State: state, DetailsURL: item.URL})
	}
	roll, failures := buildRollup(checks)
	return StatusResult{Rollup: roll, Checks: checks, Failures: failures}, nil
}

func bitbucketCheckState(state string) (CheckState, bool) {
	switch strings.ToUpper(state) {
	case "SUCCESSFUL":
		return CheckSuccess, true
	case "FAILED":
		return CheckFailure, true
	case "INPROGRESS":
		return CheckPending, true
	case "STOPPED":
		return CheckNeutral, true
	default:
		return "", false
	}
}
