// Package github holds the GitHub adapter for Beacon.
//
// BatchProvider is the real CI provider for the W1 cycle. It reads every active
// Watch in one GraphQL batch, adds one alias per distinct (repo, baseRef) so a
// failure already on main can be attributed to the base, and merges only after
// a fresh mergeability read. The existing GraphQL client, REST client, point
// counter and suite cache are reused; singleflight and a token bucket keep
// concurrent callers from duplicating one PR or one batch.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// defaultBaseRef is used when a Watch was registered without a base ref.
	defaultBaseRef = "main"

	// batchRate and batchBurst pace the client beyond the two-request semaphore
	// held by the GraphQL client. One batch, one ping and one merge each take a
	// token.
	batchRate  = rate.Limit(2)
	batchBurst = 4

	// Hold reasons returned by MergePR. They are machine readable so the caller
	// can decide whether to wake the executor or report an error.
	MergeHeldConflict     = "conflict"
	MergeHeldDirty        = "dirty"
	MergeHeldMergeable    = "mergeable_unknown"
	MergeHeldMergeState   = "merge_state_unknown"
	MergeHeldClosed       = "closed"
	MergeHeldRefused      = "merge_refused"
	MergeHeldPending      = "merge_pending"
	MergeHeldNotMergeable = "not_mergeable"
	MergeHeldInvalidRepo  = "invalid_repo"
	MergeHeldNoGraphQL    = "no_graphql_client"
	MergeHeldNoRestClient = "no_rest_client"
)

// Runner executes a local command. The default runs gh through os/exec; tests
// inject a recorder so the explicit --repo argument can be asserted without a
// network or a gh installation.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// BatchOption configures a BatchProvider.
type BatchOption func(*BatchProvider)

// WithRunner switches merges from the REST API to `gh pr merge` through the
// supplied runner. The command always carries `--repo owner/repo`.
func WithRunner(r Runner) BatchOption {
	return func(b *BatchProvider) { b.runner = r }
}

// WithBatchLimiter replaces the default token bucket.
func WithBatchLimiter(l *rate.Limiter) BatchOption {
	return func(b *BatchProvider) { b.limiter = l }
}

// WithMergeMethod selects the REST merge method. It defaults to squash.
func WithMergeMethod(method string) BatchOption {
	return func(b *BatchProvider) {
		if method != "" {
			b.mergeMethod = method
		}
	}
}

// WithBatchCache replaces the suite cache. A nil cache disables caching.
func WithBatchCache(c *Cache) BatchOption {
	return func(b *BatchProvider) { b.cache = c }
}

// batchGroup is the singleflight dependency used by FetchBatch and
// pingMergeable. Keeping it as an interface lets a test substitute a wrapper
// that observes when every caller has joined the group for a key.
type batchGroup interface {
	Do(key string, fn func() (any, error)) (v any, err error, shared bool)
}

// BatchProvider implements domain.BatchCIProvider, domain.CIProvider and
// domain.Merger against GitHub. It is safe for concurrent use.
type BatchProvider struct {
	graph       *Client
	rest        *RestClient
	counter     *Counter
	cache       *Cache
	runner      Runner
	limiter     *rate.Limiter
	mergeMethod string

	batchSF batchGroup
	prSF    batchGroup
}

var (
	_ domain.BatchCIProvider = (*BatchProvider)(nil)
	_ domain.CIProvider      = (*BatchProvider)(nil)
	_ domain.Merger          = (*BatchProvider)(nil)
)

// NewBatchProvider wires the existing GraphQL and REST adapters into one
// provider. A nil counter falls back to the GraphQL client's counter, so the
// point budget is shared by every read and write path.
func NewBatchProvider(graph *Client, rest *RestClient, counter *Counter, opts ...BatchOption) *BatchProvider {
	if counter == nil {
		if graph != nil {
			counter = graph.counterOr()
		} else {
			counter = &Counter{now: time.Now}
		}
	}
	b := &BatchProvider{
		graph:       graph,
		rest:        rest,
		counter:     counter,
		cache:       NewCache(),
		limiter:     rate.NewLimiter(batchRate, batchBurst),
		mergeMethod: "squash",
		batchSF:     &singleflight.Group{},
		prSF:        &singleflight.Group{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// PointStats reports the points spent so far and the hourly budget. It is the
// caller's signal for whether another pinpoint read is affordable.
func (b *BatchProvider) PointStats() (used int, budget int) {
	if b.counter == nil {
		return 0, BudgetPerHour
	}
	return b.counter.Used(), b.counter.Budget()
}

// FailureExcerpt makes BatchProvider implement the MCP failure-excerpt source
// port (W11-B). It delegates to the REST client and is safe to call even when
// no REST client was configured: the answer is then simply empty. Caching is
// owned by beacon_get_result, so repeated polls do not reach this method once
// the (watchID, headSHA, checkName) triple has been answered.
func (b *BatchProvider) FailureExcerpt(ctx context.Context, repo, headSHA, checkName string) (string, error) {
	if b == nil || b.rest == nil {
		return "", nil
	}
	return b.rest.FailureExcerpt(ctx, repo, headSHA, checkName)
}

// FetchBatch returns one snapshot per active Watch, keyed by Watch.ID. Closed
// Watches are skipped. All active PRs and all distinct base refs are fetched in
// one batched GraphQL operation, split into measured chunks by the client.
func (b *BatchProvider) FetchBatch(ctx context.Context, watches []domain.Watch) (map[string]domain.PRSnapshot, error) {
	key := "batch:" + batchSignature(watches)
	v, err, _ := b.batchSF.Do(key, func() (any, error) {
		return b.fetchBatch(ctx, watches)
	})
	if err != nil {
		return nil, err
	}
	shared, ok := v.(map[string]domain.PRSnapshot)
	if !ok {
		return nil, fmt.Errorf("batch: unexpected singleflight result %T", v)
	}
	out := make(map[string]domain.PRSnapshot, len(shared))
	for id, snap := range shared {
		out[id] = snap
	}
	return out, nil
}

func (b *BatchProvider) fetchBatch(ctx context.Context, watches []domain.Watch) (map[string]domain.PRSnapshot, error) {
	out := make(map[string]domain.PRSnapshot)
	active := make([]domain.Watch, 0, len(watches))
	for _, w := range watches {
		if w.Status != domain.StatusClosed {
			active = append(active, w)
		}
	}
	if len(active) == 0 || b.graph == nil {
		return out, nil
	}

	prAliases, err := withUniqueNames(AliasesFromWatches(active))
	if err != nil {
		return nil, err
	}
	baseAliases, _ := baseAliasesFromWatches(active)
	all := make([]Alias, 0, len(prAliases)+len(baseAliases))
	all = append(all, prAliases...)
	all = append(all, baseAliases...)

	if err := b.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	result, err := b.graph.Do(ctx, TierBatch, all)
	if err != nil {
		return nil, err
	}

	bases := make(map[string]domain.BaseBranchState, len(baseAliases))
	for _, a := range baseAliases {
		bases[baseLookupKey(a.Owner, a.Repo, a.BaseRef)] = parseBaseRefState(a, result.Data[a.Name])
	}

	// pings bounds the lazy mergeable REST call to once per PR per cycle.
	pings := make(map[string]bool)
	prs := make(map[string]domain.PRState, len(prAliases))
	seenObs := make(map[cacheKey]bool)
	for _, a := range prAliases {
		state, suites, err := b.parsePRAlias(ctx, a, result.Data[a.Name])
		if err != nil {
			return nil, err
		}
		mergeable := strings.ToUpper(state.Mergeable)
		if mergeable == "UNKNOWN" || mergeable == "" {
			pingKey := prLookupKey(a.Owner, a.Repo, a.PR)
			if !pings[pingKey] {
				pings[pingKey] = true
				_ = b.pingMergeable(ctx, a.Owner, a.Repo, a.PR, &state)
			}
		}
		prs[prLookupKey(a.Owner, a.Repo, a.PR)] = state

		obs := batchObservationFor(state, suites)
		if b.cache != nil && obs.HeadSHA != "" && !seenObs[keyFor(obs)] {
			seenObs[keyFor(obs)] = true
			if obs.Rerequest || obs.SuiteReset {
				b.cache.Invalidate(obs.HeadSHA)
			}
			if _, ok := b.cache.Lookup(obs); !ok {
				b.cache.Store(obs, domain.VerdictWatching)
			}
		}
	}

	for _, w := range active {
		owner, repo := splitRepo(w.Repo)
		pr, ok := prs[prLookupKey(owner, repo, w.PR)]
		if !ok {
			continue
		}
		ref := w.BaseRef
		if ref == "" {
			ref = defaultBaseRef
		}
		base, ok := bases[baseLookupKey(owner, repo, ref)]
		if !ok {
			base = domain.BaseBranchState{BaseRef: ref, FailedChecks: map[string]bool{}}
		}
		// Each Watch owns its maps: two Watches on one PR share the fetched
		// values but not the mutable containers.
		checks := make(map[string]domain.Check, len(pr.Checks))
		for name, check := range pr.Checks {
			checks[name] = check
		}
		pr.Checks = checks
		failed := make(map[string]bool, len(base.FailedChecks))
		for name, isFailed := range base.FailedChecks {
			failed[name] = isFailed
		}
		base.FailedChecks = failed
		// Store the base head observed in this batch on the PR. cibridge reads it
		// into the verdict snapshot so a PR behind a green main gets a rebase
		// wake with a machine-readable reason instead of keep_working.
		pr.BaseHeadSHA = batchBaseHeadSHA(base)
		out[w.ID] = domain.PRSnapshot{PR: pr, Base: base}
	}
	return out, nil
}

// PRState reads one PR through the same batched document. Concurrent callers
// asking for the same PR share one request.
func (b *BatchProvider) PRState(ctx context.Context, repo string, pr int) (domain.PRState, error) {
	if b.graph == nil {
		return domain.PRState{}, fmt.Errorf("%s", MergeHeldNoGraphQL)
	}
	owner, name := splitRepo(repo)
	key := "pr:" + prLookupKey(owner, name, pr)
	v, err, _ := b.prSF.Do(key, func() (any, error) {
		aliases := []Alias{{Name: "p0", Owner: owner, Repo: name, PR: pr}}
		if err := b.limiter.Wait(ctx); err != nil {
			return domain.PRState{}, err
		}
		result, err := b.graph.Do(ctx, TierBatch, aliases)
		if err != nil {
			return domain.PRState{}, err
		}
		state, _, err := b.parsePRAlias(ctx, aliases[0], result.Data["p0"])
		if err != nil {
			return domain.PRState{}, err
		}
		mergeable := strings.ToUpper(state.Mergeable)
		if mergeable == "UNKNOWN" || mergeable == "" {
			_ = b.pingMergeable(ctx, owner, name, pr, &state)
		}
		return state, nil
	})
	if err != nil {
		return domain.PRState{}, err
	}
	state, ok := v.(domain.PRState)
	if !ok {
		return domain.PRState{}, fmt.Errorf("batch: unexpected PR singleflight result %T", v)
	}
	return state, nil
}

// Rebase updates a stale PR branch onto its base and returns the new head SHA.
// It satisfies the cibridge.Rebaser port used by Bridge.WithAutoRebase; the
// port is not imported here because infra packages must not depend on siblings.
// A missing REST client or a malformed repo is an error, so the Bridge can keep
// its legacy wake-the-executor behaviour instead of claiming a rebase it did
// not perform.
func (b *BatchProvider) Rebase(ctx context.Context, repo string, pr int) (string, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return "", fmt.Errorf("rebase: repo %q must be owner/repo", repo)
	}
	if b == nil || b.rest == nil {
		return "", fmt.Errorf("%s", MergeHeldNoRestClient)
	}
	if err := b.limiter.Wait(ctx); err != nil {
		return "", err
	}
	return b.rest.RebasePullRequest(ctx, owner, name, pr)
}

// Merge implements domain.Merger. It is an alias for MergePR so the same
// adapter satisfies both the explicit W1-C contract and the domain port.
func (b *BatchProvider) Merge(ctx context.Context, repo string, pr int) (bool, bool, string, error) {
	return b.MergePR(ctx, repo, pr)
}

// MergePR reads fresh merge state, refuses a dirty or conflicting PR without
// an error, and only then issues a merge. A successful command is confirmed by
// a second read; an unconfirmed merge is held, never reported as merged.
func (b *BatchProvider) MergePR(ctx context.Context, repo string, pr int) (bool, bool, string, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return false, true, MergeHeldInvalidRepo,
			fmt.Errorf("merge: repo %q must be owner/repo", repo)
	}

	state, err := b.PRState(ctx, repo, pr)
	if err != nil {
		return false, false, "", err
	}
	return b.mergePRWithState(ctx, owner, name, repo, pr, state)
}

// MergePRWithState merges with a PRState the caller already read, such as the
// snapshot returned by FetchBatch. It avoids a second GraphQL read whose lazy
// mergeable field can report UNKNOWN even when the batch resolved it. An
// UNKNOWN supplied state is still pinged through pingMergeable before a merge
// is attempted, so a stale snapshot cannot bypass the mergeability guard.
func (b *BatchProvider) MergePRWithState(ctx context.Context, repo string, pr int, state domain.PRState) (bool, bool, string, error) {
	owner, name := splitRepo(repo)
	if owner == "" || name == "" {
		return false, true, MergeHeldInvalidRepo,
			fmt.Errorf("merge: repo %q must be owner/repo", repo)
	}
	return b.mergePRWithState(ctx, owner, name, repo, pr, state)
}

// mergePRWithState holds or merges after the mergeability guards pass. The
// state may come from a fresh PRState read or from a batch snapshot; both paths
// share the same policy checks, write and confirmation.
func (b *BatchProvider) mergePRWithState(ctx context.Context, owner, name, repo string, pr int, state domain.PRState) (bool, bool, string, error) {
	switch strings.ToUpper(state.State) {
	case "MERGED":
		return true, false, "", nil
	case "CLOSED":
		return false, true, MergeHeldClosed, nil
	}

	mergeable := strings.ToUpper(state.Mergeable)
	if mergeable == "UNKNOWN" || mergeable == "" {
		_ = b.pingMergeable(ctx, owner, name, pr, &state)
		mergeable = strings.ToUpper(state.Mergeable)
	}
	switch mergeable {
	case "MERGEABLE":
	case "UNKNOWN", "":
		return false, true, MergeHeldMergeable, nil
	case "CONFLICTING":
		return false, true, MergeHeldConflict, nil
	default:
		return false, true, MergeHeldNotMergeable, nil
	}

	switch strings.ToUpper(state.MergeStateStatus) {
	case "CLEAN", "HAS_HOOKS", "BEHIND":
	case "DIRTY", "BLOCKED":
		return false, true, MergeHeldDirty, nil
	case "UNKNOWN", "":
		return false, true, MergeHeldMergeState, nil
	default:
		return false, true, strings.ToLower(state.MergeStateStatus), nil
	}

	if err := b.limiter.Wait(ctx); err != nil {
		return false, false, "", err
	}
	if b.runner != nil {
		return b.mergeWithRunner(ctx, owner, name, pr)
	}
	if b.rest == nil {
		return false, true, MergeHeldNoRestClient, nil
	}

	result, err := b.rest.MergePullRequest(ctx, owner, name, pr, b.mergeMethod)
	if err != nil {
		return false, false, "", err
	}
	if !result.Merged {
		return false, true, result.Reason, nil
	}
	return b.confirmMerge(ctx, repo, pr)
}

func (b *BatchProvider) mergeWithRunner(ctx context.Context, owner, repo string, pr int) (bool, bool, string, error) {
	// --repo is mandatory: the working directory must never select the target.
	args := []string{"pr", "merge", strconv.Itoa(pr), "--repo", owner + "/" + repo,
		"--squash", "--delete-branch"}
	out, err := b.runner.Run(ctx, "gh", args...)
	if err != nil {
		hint := strings.ToLower(string(out) + " " + err.Error())
		if strings.Contains(hint, "not mergeable") || strings.Contains(hint, "conflict") ||
			strings.Contains(hint, "failed to merge") || strings.Contains(hint, "head branch was modified") {
			return false, true, MergeHeldRefused, nil
		}
		if strings.Contains(hint, "already in progress") {
			// GitHub accepted the merge and is still applying it. A second
			// `gh pr merge` only adds noise, so read the resulting state. The
			// caller closes the Watch only if state is already MERGED.
			return b.confirmMerge(ctx, owner+"/"+repo, pr)
		}
		return false, false, "", fmt.Errorf("gh merge %s/%s#%d: %w", owner, repo, pr, err)
	}
	return b.confirmMerge(ctx, owner+"/"+repo, pr)
}

func (b *BatchProvider) confirmMerge(ctx context.Context, repo string, pr int) (bool, bool, string, error) {
	after, err := b.PRState(ctx, repo, pr)
	if err != nil {
		return false, false, "", err
	}
	if strings.EqualFold(after.State, "MERGED") {
		return true, false, "", nil
	}
	return false, true, MergeHeldPending, nil
}

// mergePatch is the part of a PRState that a mergeability ping can resolve.
// The singleflight result carries a patch rather than mutating a caller-owned
// state, so every caller applies the same answer to its own state.
type mergePatch struct {
	mergeable        string
	mergeStateStatus string
}

func patchFromMergeState(merged MergeState) mergePatch {
	p := mergePatch{}
	if merged.Mergeable != nil {
		if *merged.Mergeable {
			p.mergeable = "MERGEABLE"
		} else {
			p.mergeable = "CONFLICTING"
		}
	}
	if merged.MergeableState != "" {
		p.mergeStateStatus = strings.ToUpper(merged.MergeableState)
	}
	return p
}

func (p mergePatch) apply(state *domain.PRState) {
	if p.mergeable != "" {
		state.Mergeable = p.mergeable
	}
	if p.mergeStateStatus != "" {
		state.MergeStateStatus = p.mergeStateStatus
	}
}

// pingMergeable resolves GitHub's lazy mergeable field through one REST read
// and applies the answer to state. The singleflight result is shared, so each
// caller applies the patch to its own state instead of hoping the leader
// closure captured its pointer. A 304, or a reply that still leaves mergeable
// unset, is retried without If-None-Match: a cached ETag can hide the
// UNKNOWN -> MERGEABLE transition that makes an otherwise green PR mergeable.
// It returns the singleflight shared flag so a test can assert that callers
// joined one in-flight group.
func (b *BatchProvider) pingMergeable(ctx context.Context, owner, repo string, pr int, state *domain.PRState) bool {
	if b.rest == nil || state == nil {
		return false
	}
	key := "ping:" + prLookupKey(owner, repo, pr)
	v, _, shared := b.prSF.Do(key, func() (any, error) {
		if err := b.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		merged, notModified, err := b.rest.PingMergeable(ctx, owner, repo, pr)
		if err != nil {
			return nil, err
		}
		if notModified || merged.Mergeable == nil {
			// The conditional reply is not definitive. Re-read without the
			// cached ETag so the computed value is visible to this cycle.
			fresh, err := b.rest.PingMergeableForce(ctx, owner, repo, pr)
			if err != nil {
				return nil, err
			}
			merged = fresh
		}
		return patchFromMergeState(merged), nil
	})
	if patch, ok := v.(mergePatch); ok {
		patch.apply(state)
	}
	return shared
}

// parsePRAlias maps one TierBatch PR payload into a PRState. It follows
// pageInfo so a PR with more than 50 contexts is not silently truncated.
func (b *BatchProvider) parsePRAlias(ctx context.Context, alias Alias, raw json.RawMessage) (domain.PRState, []ContextSuite, error) {
	// The alias resolves to a repository object, so the PR fields sit one
	// level deeper under pullRequest (see unwrapPullRequestAlias).
	raw = unwrapPullRequestAlias(raw)
	var pr tier1PR
	if err := json.Unmarshal(raw, &pr); err != nil {
		return domain.PRState{}, nil, fmt.Errorf("batch %s: decode PR: %w", alias.Name, err)
	}
	checks, suites, page, err := parseTier2Payload(raw)
	if err != nil {
		return domain.PRState{}, nil, fmt.Errorf("batch %s: %w", alias.Name, err)
	}
	for pages := 1; page.HasNextPage; pages++ {
		if page.EndCursor == "" {
			return domain.PRState{}, nil, fmt.Errorf("batch %s: hasNextPage without endCursor", alias.Name)
		}
		if pages >= maxTier2Pages {
			return domain.PRState{}, nil, fmt.Errorf("batch %s: exceeded %d pages", alias.Name, maxTier2Pages)
		}
		next, err := b.graph.postTier2Query(ctx, BuildTier2PageQuery(alias, page.EndCursor))
		if err != nil {
			return domain.PRState{}, nil, err
		}
		nextChecks, nextSuites, nextPage, err := parseTier2Payload(unwrapPullRequestAlias(next.Data[alias.Name]))
		if err != nil {
			return domain.PRState{}, nil, fmt.Errorf("batch %s next page: %w", alias.Name, err)
		}
		for name, check := range nextChecks {
			checks[name] = check
		}
		suites = append(suites, nextSuites...)
		page = nextPage
	}

	normalized := make(map[string]domain.Check, len(checks))
	anyRequired := false
	for name, check := range checks {
		mapped := normalizeGitHubCheck(check.Conclusion, check.State)
		mapped.Required = check.Required
		mapped.DetailsURL = check.DetailsURL
		normalized[name] = mapped
		if check.Required {
			anyRequired = true
		}
	}
	// GitHub's isRequired(pullRequestNumber:) returns false for every context
	// when the base branch has no required status checks and no rulesets. In
	// that case an unqualified false cannot mean "this check was explicitly
	// excluded", so trust the observed set as the required list. Checks named
	// in ATX-C01 stay informational. Repositories with protection are left
	// exactly as GitHub reported them.
	if !anyRequired && len(normalized) > 0 {
		for name, check := range normalized {
			if domain.IsInformationalCheckName(name) {
				continue
			}
			check.Required = true
			normalized[name] = check
		}
	}
	state := domain.PRState{
		State:            pr.State,
		IsDraft:          pr.IsDraft,
		HeadRefOid:       pr.HeadRefOid,
		BaseRefName:      pr.BaseRefName,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
		ReviewDecision:   pr.ReviewDecision,
		Checks:           normalized,
	}
	return state, suites, nil
}

// normalizeGitHubCheck maps the raw GitHub fields into the states the verdict
// engine reads. CheckRun contributes conclusion, StatusContext contributes
// state; when both are present conclusion wins. The PASS and FAIL aliases come
// from domain.NormalizeConclusion, so this adapter keeps one vocabulary with
// the verdict engine.
func normalizeGitHubCheck(conclusion, state string) domain.Check {
	raw := conclusion
	if raw == "" {
		raw = state
	}
	fromRun := conclusion != ""
	canonical := domain.NormalizeConclusion(raw)
	// A check run with ACTION_REQUIRED waits on a human. It stays pending here
	// even though the domain vocabulary treats it as a failure.
	if strings.EqualFold(strings.TrimSpace(raw), "ACTION_REQUIRED") {
		canonical = "PENDING"
	}
	switch canonical {
	case "SUCCESS":
		if fromRun {
			return domain.Check{State: "COMPLETED", Conclusion: "SUCCESS"}
		}
		return domain.Check{State: "SUCCESS", Conclusion: "SUCCESS"}
	case "FAILURE", "CANCELLED":
		// CANCELLED and CANCELED reach this branch through the domain mapping.
		// Both remain failed checks, as they did before the shared vocabulary.
		if fromRun {
			return domain.Check{State: "COMPLETED", Conclusion: "FAILURE"}
		}
		return domain.Check{State: "FAILURE", Conclusion: "FAILURE"}
	default:
		// QUEUED, IN_PROGRESS, WAITING, REQUESTED and unknown values stay
		// pending instead of blocking an unrelated PR.
		return domain.Check{State: "PENDING"}
	}
}

// baseRefPayload mirrors the ref(qualifiedName) selection in baseRefFields.
type baseRefPayload struct {
	Ref *struct {
		Target *struct {
			OID               string       `json:"oid"`
			StatusCheckRollup *tier2Rollup `json:"statusCheckRollup"`
		} `json:"target"`
	} `json:"ref"`
}

// batchBaseHeadSHA returns the base ref OID read by baseRefFields in the same
// batch as the PR. The companion behind signal is already carried by the PR
// selection as mergeStateStatus=BEHIND (GitHub's behindBy > 0), so no extra
// GraphQL field is needed to tell a stale base from a genuine PR failure.
func batchBaseHeadSHA(base domain.BaseBranchState) string {
	return strings.TrimSpace(base.HeadSHA)
}

// parseBaseRefState reads the failed checks of a base branch. An absent ref or
// rollup leaves ChecksKnown false, so the caller cannot claim main is green.
func parseBaseRefState(alias Alias, raw json.RawMessage) domain.BaseBranchState {
	state := domain.BaseBranchState{
		BaseRef:      alias.BaseRef,
		FailedChecks: map[string]bool{},
	}
	if len(raw) == 0 || string(raw) == "null" {
		return state
	}
	var doc baseRefPayload
	if err := json.Unmarshal(raw, &doc); err != nil {
		return state
	}
	if doc.Ref == nil || doc.Ref.Target == nil {
		return state
	}
	state.HeadSHA = doc.Ref.Target.OID
	rollup := doc.Ref.Target.StatusCheckRollup
	if rollup == nil || rollup.Contexts == nil {
		return state
	}
	state.ChecksKnown = true
	for _, node := range rollup.Contexts.Nodes {
		name, check, ok := mapTier2Node(node)
		if !ok {
			continue
		}
		mapped := normalizeGitHubCheck(check.Conclusion, check.State)
		if mapped.Conclusion == "FAILURE" || mapped.State == "FAILURE" {
			state.FailedChecks[name] = true
		}
	}
	return state
}

func batchObservationFor(pr domain.PRState, suites []ContextSuite) Observation {
	contexts := make([]SuiteContext, 0, len(suites))
	for _, s := range suites {
		contexts = append(contexts, SuiteContext{Name: s.Name, SuiteID: s.SuiteID})
	}
	return Observation{
		HeadSHA:     pr.HeadRefOid,
		Contexts:    contexts,
		SuitesValid: true,
	}
}

// baseAliasesFromWatches emits one alias per distinct repository/base-ref pair.
func baseAliasesFromWatches(watches []domain.Watch) ([]Alias, map[string]Alias) {
	seen := make(map[string]Alias)
	for _, w := range watches {
		owner, repo := splitRepo(w.Repo)
		ref := w.BaseRef
		if ref == "" {
			ref = defaultBaseRef
		}
		key := baseLookupKey(owner, repo, ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = Alias{
			Name:    "b" + strconv.Itoa(len(seen)),
			Owner:   owner,
			Repo:    repo,
			BaseRef: ref,
		}
	}
	out := make([]Alias, 0, len(seen))
	for _, a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, seen
}

func batchSignature(watches []domain.Watch) string {
	ids := make([]string, 0, len(watches))
	for _, w := range watches {
		ids = append(ids, w.ID+":"+w.Repo+"#"+strconv.Itoa(w.PR)+":"+w.HeadSHA+":"+w.BaseRef)
	}
	sort.Strings(ids)
	return strings.Join(ids, "|")
}

func prLookupKey(owner, repo string, pr int) string {
	return owner + "/" + repo + "#" + strconv.Itoa(pr)
}

func baseLookupKey(owner, repo, ref string) string {
	return owner + "/" + repo + "\x00" + ref
}
