// Package mcp hosts Beacon's MCP server built on the official go-sdk.
//
// This file implements beacon_get_result (WORKER-PROMPT §4.1): an additive tool
// that hands out a Watch result and stamps readAt on the caller's obligation.
// It never returns full logs or raw check output, only names, conclusions,
// links, a bounded redacted annotation excerpt (W11-B) and the flaky/retry
// facts needed to decide the next step (WORKER-PROMPT §4.1, ADR 0012). The tool
// is additive, not read-only: issuing content is a state
// change even though the response is a projection of the Watch.
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// GetResultInput is the argument object of beacon_get_result. includeChecks is
// accepted for input-contract compatibility; full checks and logs are never
// emitted, so it does not change the response shape.
type GetResultInput struct {
	WatchID       string `json:"watchID" jsonschema:"identifier of the Watch to read"`
	IncludeChecks *bool  `json:"includeChecks,omitempty" jsonschema:"accepted for compatibility; full checks and logs are never returned"`
}

// failureConclusions are the check-level conclusions that make a check a failed
// check. SKIPPED, NEUTRAL and the pending states (WAITING, QUEUED,
// ACTION_REQUIRED) are deliberately absent: WORKER-PROMPT §4.6 allows them.
// CANCELLED is a failure too: an aborted check is not a passing check (T3.401).
var failureConclusions = map[string]bool{
	"FAILURE":         true,
	"ERROR":           true,
	"TIMED_OUT":       true,
	"STARTUP_FAILURE": true,
	"CANCELLED":       true,
}

// FailureExcerpt is the bounded answer of a failure-excerpt source. W11-B adds
// it to beacon_get_result so a subscriber reads the reason for a failed check
// from the tool instead of shelling out to `gh run view --log-failed`. The
// source is fetched at most once per (watchID, headSHA, checkName) and cached
// in SQLite; a second poll in the same cycle never touches the network again.
type FailureExcerptSource interface {
	// FailureExcerpt returns a short annotation excerpt for one failed check.
	// The handler redacts and escapes it before it reaches the wire. An empty
	// string with a nil error means the check has no annotations; it is cached
	// like any other answer so it is not re-fetched.
	FailureExcerpt(ctx context.Context, repo, headSHA, checkName string) (string, error)
}

// failureExcerptLimit is the 2 KB ceiling of a single excerpt and of the
// aggregate string returned in GetResultOutput.
const failureExcerptLimit = 2048

// failureExcerptTruncatedMarker is appended when the byte ceiling cut an
// excerpt. It stays in the cached string, so a cache hit still shows the loss.
const failureExcerptTruncatedMarker = "...[truncated]"

// failureExcerptAnnotationCount bounds how many annotations are requested and
// rendered per failed check.
const failureExcerptAnnotationCount = 8

// The ceilings bound the failed-check array and the encoded result response.
const (
	getResultMaxRecords = 200
	getResultMaxBytes   = 256 * 1024
)

const getResultNarrowHint = "narrow the request by reading a watchID with fewer failedChecks; includeChecks does not reduce the response"

// resultStoreReader is the narrow read port used by beacon_get_result and
// beacon_ack. It is satisfied by the store and by test doubles. The core only
// reads; every write goes through ObligationStamper.
type resultStoreReader interface {
	Watch(id string) (domain.Watch, error)
	Obligation(id string) (domain.Obligation, error)
}

// resultStoreNotFound is an optional capability of a read port. A port whose
// missing row arrives as a plain sentinel implements it so a caller can tell
// an empty selection from a storage failure. The production store returns an
// AppError for a missing row and does not implement this interface.
type resultStoreNotFound interface {
	IsNotFound(err error) bool
}

// resultTxWriter is the single-writer port used to stamp a delivery fact. The
// production store satisfies it with one SQLite transaction per stamp.
type resultTxWriter interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// failureExcerptCache stores one excerpt per (watchID, headSHA, checkName). It
// is deliberately a port so a test can assert that a second read of the same
// triple never reaches the source again.
type failureExcerptCache interface {
	Get(ctx context.Context, watchID, headSHA, checkName string) (string, bool, error)
	Put(ctx context.Context, watchID, headSHA, checkName, excerpt string) error
}

// failureExcerptTableDDL is idempotent: the cache owns its own table so a
// caller does not need a migration just to enable excerpts. Reads go through
// the same single writer as stamps, which keeps the cache consistent with the
// Watch it describes.
const failureExcerptTableDDL = `CREATE TABLE IF NOT EXISTS failure_excerpts (
	watch_id   TEXT NOT NULL,
	head_sha   TEXT NOT NULL,
	check_name TEXT NOT NULL,
	excerpt    TEXT NOT NULL,
	PRIMARY KEY (watch_id, head_sha, check_name)
)`

// storeExcerptCache is the capability the store exposes for its own
// failure_excerpts table. The method names differ from failureExcerptCache so
// the two ports stay separate; the adapter below bridges them. Keeping the
// capability duck-typed lets this package avoid importing the store (ADR 0005).
type storeExcerptCache interface {
	FailureExcerptGet(ctx context.Context, watchID, headSHA, checkName string) (string, bool, error)
	FailureExcerptPut(ctx context.Context, watchID, headSHA, checkName, excerpt string) error
}

type adapterExcerptCache struct {
	store storeExcerptCache
}

func (a *adapterExcerptCache) Get(ctx context.Context, watchID, headSHA, checkName string) (string, bool, error) {
	return a.store.FailureExcerptGet(ctx, watchID, headSHA, checkName)
}

func (a *adapterExcerptCache) Put(ctx context.Context, watchID, headSHA, checkName, excerpt string) error {
	return a.store.FailureExcerptPut(ctx, watchID, headSHA, checkName, excerpt)
}

// sqlFailureExcerptCache implements failureExcerptCache over the store's single
// writer. The stored text is already redacted, escaped and bounded before it
// reaches SQLite, so a cache hit needs no further sanitising. This fallback
// serves callers whose store does not expose the cache capability yet.
type sqlFailureExcerptCache struct {
	w resultTxWriter
}

func newSQLFailureExcerptCache(w resultTxWriter) *sqlFailureExcerptCache {
	if w == nil {
		return nil
	}
	return &sqlFailureExcerptCache{w: w}
}

func (c *sqlFailureExcerptCache) Get(ctx context.Context, watchID, headSHA, checkName string) (string, bool, error) {
	if c == nil || c.w == nil {
		return "", false, nil
	}
	var (
		excerpt string
		found   bool
	)
	err := c.w.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(failureExcerptTableDDL); err != nil {
			return err
		}
		err := tx.QueryRow(`SELECT excerpt FROM failure_excerpts
			WHERE watch_id = ? AND head_sha = ? AND check_name = ?`,
			watchID, headSHA, checkName).Scan(&excerpt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return excerpt, found, err
}

func (c *sqlFailureExcerptCache) Put(ctx context.Context, watchID, headSHA, checkName, excerpt string) error {
	if c == nil || c.w == nil {
		return nil
	}
	return c.w.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(failureExcerptTableDDL); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO failure_excerpts
			(watch_id, head_sha, check_name, excerpt) VALUES (?, ?, ?, ?)
			ON CONFLICT(watch_id, head_sha, check_name) DO NOTHING`,
			watchID, headSHA, checkName, excerpt)
		return err
	})
}

// obligationStamp selects which delivery fact a stamp writes.
type obligationStamp int

const (
	stampRead obligationStamp = iota
	stampClaimed
)

// ObligationStamper writes one delivery fact on an obligation atomically. The
// boolean reports whether the fact was newly written; a false value with a nil
// error means the fact was already present, which is the idempotent repeat.
// ADR 0014 and WORKER-PROMPT §4.3 require read and claim to stay distinct.
type ObligationStamper interface {
	StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
	StampClaimed(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error)
}

// SQLObligationStamper implements ObligationStamper against the single-writer
// store. It reads and rewrites the obligation payload inside one transaction,
// so a client is only answered after the fact reached disk (ADR 0015).
type SQLObligationStamper struct {
	w resultTxWriter
}

// NewSQLObligationStamper builds a stamper over the store writer.
func NewSQLObligationStamper(w resultTxWriter) *SQLObligationStamper {
	return &SQLObligationStamper{w: w}
}

// StampRead writes readAt when it is still zero.
func (s *SQLObligationStamper) StampRead(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	return s.stamp(ctx, id, stampRead, at)
}

// StampClaimed writes claimedAt when it is still zero.
func (s *SQLObligationStamper) StampClaimed(ctx context.Context, id string, at time.Time) (domain.Obligation, bool, error) {
	return s.stamp(ctx, id, stampClaimed, at)
}

func (s *SQLObligationStamper) stamp(ctx context.Context, id string, field obligationStamp, at time.Time) (domain.Obligation, bool, error) {
	if s == nil || s.w == nil {
		return domain.Obligation{}, false, domain.NewAppError(domain.CodePersistFailed, "result: store writer is nil")
	}
	if id == "" {
		return domain.Obligation{}, false, domain.NewAppError(domain.CodeInvalidInput, "result: obligationID is required")
	}

	out := domain.Obligation{}
	applied := false
	err := s.w.Write(ctx, func(tx *sql.Tx) error {
		var payload []byte
		if err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.NewAppError(domain.CodeWatchNotFound, "result: obligation not found")
			}
			return err
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return err
		}
		switch field {
		case stampRead:
			if !o.ReadAt.IsZero() {
				out = o
				return nil
			}
			o.ReadAt = at
		case stampClaimed:
			if !o.ClaimedAt.IsZero() {
				out = o
				return nil
			}
			o.ClaimedAt = at
		}
		next, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE obligations SET payload = ? WHERE id = ?`, next, id); err != nil {
			return err
		}
		out = o
		applied = true
		return nil
	})
	if err != nil {
		var appErr *domain.AppError
		if errors.As(err, &appErr) {
			return out, applied, err
		}
		return out, applied, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	return out, applied, nil
}

// GetResultHandler implements beacon_get_result over the read port and the
// transactional stamper. One handler owns a clock so repeated reads observe a
// single, caller-controlled notion of "now".
type GetResultHandler struct {
	store   resultStoreReader
	stamper ObligationStamper
	clock   domain.Clock
	after   time.Duration

	// W11-B failure-excerpt plumbing. Both fields are optional: a nil source
	// omits failureExcerpt entirely, and a nil cache falls back to the store's
	// single writer when the stamper is SQL-backed.
	excerptSource FailureExcerptSource
	excerptCache  failureExcerptCache
}

// NewGetResultHandler builds a handler. A nil clock falls back to wall time.
func NewGetResultHandler(store resultStoreReader, stamper ObligationStamper, clock domain.Clock) *GetResultHandler {
	if clock == nil {
		clock = getResultWallClock{}
	}
	return &GetResultHandler{
		store:         store,
		stamper:       stamper,
		clock:         clock,
		excerptSource: newEnvFailureExcerptSource(),
	}
}

// SetFailureExcerptSource supplies the port that fetches annotations for a
// failed check. A nil source disables the W11-B field altogether, so a server
// without GitHub credentials keeps the exact previous response shape.
func (h *GetResultHandler) SetFailureExcerptSource(src FailureExcerptSource) {
	if h != nil {
		h.excerptSource = src
	}
}

// SetFailureExcerptCache overrides the excerpt cache. Tests inject a counting
// cache here to prove a repeated read does not call the source twice.
func (h *GetResultHandler) SetFailureExcerptCache(cache failureExcerptCache) {
	if h != nil {
		h.excerptCache = cache
	}
}

// excerptStore derives the cache from the store's single writer when none was
// injected. The production store implements Write directly; a bare SQL stamper
// is the fallback for callers that pass only a stamper. Keeping this derivation
// here avoids a new composition-root port just to persist one short string.
func (h *GetResultHandler) excerptStore() failureExcerptCache {
	if h == nil {
		return nil
	}
	if h.excerptCache != nil {
		return h.excerptCache
	}
	if s, ok := h.store.(storeExcerptCache); ok {
		return &adapterExcerptCache{store: s}
	}
	if w, ok := h.store.(resultTxWriter); ok {
		return newSQLFailureExcerptCache(w)
	}
	if s, ok := h.stamper.(*SQLObligationStamper); ok && s != nil {
		return newSQLFailureExcerptCache(s.w)
	}
	return nil
}

// SetAfter sets the MCP-boundary threshold that turns a slow call into the
// dialect's task/job form. Zero keeps the classical response.
func (h *GetResultHandler) SetAfter(after time.Duration) {
	if h != nil {
		h.after = after
	}
}

type getResultWallClock struct{}

func (getResultWallClock) Now() time.Time { return time.Now() }

// Handle adapts Result to the go-sdk typed tool handler signature. Domain
// errors are returned as-is for ToErrorShape to convert at the MCP boundary.
func (h *GetResultHandler) Handle(ctx context.Context, req *mcpsdk.CallToolRequest, in GetResultInput) (*mcpsdk.CallToolResult, GetResultOutput, error) {
	start := time.Time{}
	if h != nil {
		start = h.clock.Now()
	}
	out, err := h.Result(ctx, req, in)
	if err != nil {
		return nil, GetResultOutput{}, err
	}
	// The core projection above is version-blind. Only this boundary layer
	// chooses the wire form, using the client's capabilities and the --after
	// threshold (ADR 0031 §2, ADR 0003).
	if h != nil {
		out = ApplyTaskForm(out, DialectFromRequest(req), h.clock.Now().Sub(start), h.after)
	}
	return nil, out, nil
}

// nextStepFor maps a verdict to the one action the subscriber should take. The
// mapping is a pure function of the verdict so the same result always teaches
// the same next step, and a client never has to parse a sentence to branch.
func nextStepFor(verdict domain.Verdict) (action, command, hint string) {
	switch verdict {
	case domain.VerdictKeepWorking:
		return NextActionKeepWorking, "",
			"do not rebase an unchanged head; fix only the failedChecks returned above"
	case domain.VerdictRebase:
		return NextActionRebase, RebaseCommand,
			"origin/main moved; rebase and push with lease before waiting again"
	case domain.VerdictRetry:
		return NextActionRetry, RetryCommand,
			"the failure is flaky or cancelled; rerun only the failed jobs"
	case domain.VerdictBlocked:
		return NextActionBlocked, "",
			"the failure is on main; stop and wait for the blocker to be resolved, do not fix main"
	case domain.VerdictReady:
		return NextActionWait, "",
			"checks are green; the orchestrator merges, no command is needed here"
	case domain.VerdictTimeout:
		// Terminal verdict, not the retryable long-poll reason. The two used to
		// share the "timeout" string and gave opposite instructions (BCX-11).
		return domain.NextActionDone, "",
			"the Watch hit a terminal timeout verdict; stop here and do not retry the long-poll wait"
	case domain.VerdictMerged, domain.VerdictClosed:
		return NextActionWait, "", "the Watch is finished; there is no next step"
	default:
		return NextActionWait, "", "no verdict yet; finish the turn with waiting, watchID"
	}
}

// Result reads the Watch, finds the caller's obligation at the current
// revision, stamps readAt, and projects the contract fields. It works for any
// verdict, including ready (WORKER-PROMPT §4.1). The caller is identified by its
// opaque sessionID from _meta; the core never interprets that value.
func (h *GetResultHandler) Result(ctx context.Context, req *mcpsdk.CallToolRequest, in GetResultInput) (GetResultOutput, error) {
	if in.WatchID == "" {
		return GetResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "get_result: watchID is required")
	}
	if h == nil || h.store == nil || h.stamper == nil {
		return GetResultOutput{}, domain.NewAppError(domain.CodePersistFailed, "get_result: store or stamper is nil")
	}

	w, err := h.store.Watch(in.WatchID)
	if err != nil {
		observe.ActorError("get_result", in.WatchID, err)
		return GetResultOutput{}, mapResultReadFailure(h.store, err, "get_result: watch")
	}

	caller := SessionIDFromRequest(req)
	if caller == nil || *caller == "" {
		return GetResultOutput{}, domain.NewAppError(domain.CodeInvalidInput, "get_result: sessionID from _meta is required")
	}
	obligationID := domain.ObligationKey(w.ID, w.Revision, *caller)
	if _, err := h.store.Obligation(obligationID); err != nil {
		return GetResultOutput{}, mapResultReadFailure(h.store, err, "get_result: obligation")
	}

	failed := collectFailedChecks(w)
	excerpt, excerptTruncated, excerptLogURL, excerptUnavailable := h.attachFailureExcerpts(ctx, w, failed)
	action, command, hint := nextStepFor(w.Verdict)
	// The projection fills the seven required fields and the optional ones the
	// Watch supports. ADR 0094 lists the full field set and each field's purpose.
	out := GetResultOutput{
		Status:                    string(w.Status),
		Verdict:                   string(w.Verdict),
		Revision:                  w.Revision,
		Merge:                     MergeSnapshot{Mergeable: w.Merge.Mergeable, MergeStateStatus: w.Merge.MergeStateStatus},
		FailedChecks:              failed,
		ObligationID:              obligationID,
		RetryBudget:               remainingRetryBudget(w, failed),
		NextAction:                action,
		NextCommand:               command,
		NextHint:                  hint,
		FailureExcerpt:            excerpt,
		FailureExcerptTruncated:   excerptTruncated,
		FailureExcerptLogURL:      excerptLogURL,
		FailureExcerptUnavailable: excerptUnavailable,
		ConfigID:                  w.ConfigID,
	}

	// Refuse before the read stamp: a response the caller never receives must
	// not consume the obligation's single readAt fact.
	limits := responseLimits{records: getResultMaxRecords, bytes: getResultMaxBytes}
	if err := checkResponseLimits("beacon_get_result", "failedCheck", len(out.FailedChecks), limits, getResultNarrowHint, out); err != nil {
		return GetResultOutput{}, err
	}

	if _, _, err := h.stamper.StampRead(ctx, obligationID, h.clock.Now()); err != nil {
		observe.ActorError("get_result-stamp", in.WatchID, err)
		return GetResultOutput{}, err
	}
	observe.ResultRead(w.ID, string(w.Verdict), w.Revision)
	return out, nil
}

// --- Failure excerpts (W11-B) ----------------------------------------------
//
// A failed check alone does not tell a subscriber what broke. Before W11-B the
// subscriber had to run `gh run view --log-failed` itself. The handler now asks
// a FailureExcerptSource once per (watchID, headSHA, checkName), caches the
// answer in SQLite, redacts and escapes it at this boundary, and returns it in
// failureExcerpt plus the matching failedChecks entry. The field is optional:
// with no source configured the response is byte-for-byte the old shape.

// attachFailureExcerpts fills each failed check's excerpt and returns the
// aggregate bounded at failureExcerptLimit plus a truncation flag, a link to
// the full CI log and a flag reporting that at least one source fetch failed
// (EH-02). A cache hit never calls the source; a miss calls it once and stores
// a successful answer, including an empty one, so a check with no annotations
// is not re-fetched on the next poll. A failed fetch is never stored: an empty
// cached string would look like a check with no annotations and would erase the
// unavailability flag on the next poll. The truncation marker lives in the
// cached string, so a cache hit keeps that flag too.
func (h *GetResultHandler) attachFailureExcerpts(ctx context.Context, w domain.Watch, failed []FailedCheck) (string, bool, string, bool) {
	if h == nil || h.excerptSource == nil || len(failed) == 0 {
		return "", false, "", false
	}
	cache := h.excerptStore()
	var agg strings.Builder
	truncated := false
	logURL := ""
	unavailable := false
	for i := range failed {
		name := failed[i].Name
		excerpt, ok, err := cacheGet(ctx, cache, w.ID, w.HeadSHA, name)
		if err != nil {
			observe.ActorError("get_result-excerpt-cache", w.ID, err)
			ok = false
		}
		if !ok {
			raw, fetchErr := h.excerptSource.FailureExcerpt(ctx, w.Repo, w.HeadSHA, name)
			if fetchErr != nil {
				observe.ActorError("get_result-excerpt", w.ID, fetchErr)
				failed[i].FailureExcerptUnavailable = true
				unavailable = true
				raw = ""
			}
			excerpt = sanitizeExcerpt(prepareWakeExcerpt(raw), failureExcerptLimit)
			if fetchErr == nil {
				if putErr := cachePut(ctx, cache, w.ID, w.HeadSHA, name, excerpt); putErr != nil {
					observe.ActorError("get_result-excerpt-cache", w.ID, putErr)
				}
			}
		}
		failed[i].FailureExcerpt = excerpt
		if excerptWasTruncated(excerpt) {
			truncated = true
			if logURL == "" {
				logURL = failed[i].DetailsURL
			}
		}
		if excerpt == "" || agg.Len() >= failureExcerptLimit {
			continue
		}
		if agg.Len() > 0 {
			agg.WriteByte('\n')
		}
		agg.WriteString(name)
		agg.WriteString(": ")
		agg.WriteString(excerpt)
	}
	aggregate := prepareWakeExcerpt(agg.String())
	if excerptWasTruncated(aggregate) {
		truncated = true
	}
	if aggregate != "" && logURL == "" && len(failed) > 0 {
		logURL = failed[0].DetailsURL
	}
	return aggregate, truncated, logURL, unavailable
}

// excerptWasTruncated reports whether an excerpt carries the truncation marker.
func excerptWasTruncated(excerpt string) bool {
	return strings.HasSuffix(excerpt, failureExcerptTruncatedMarker)
}

// prepareWakeExcerpt applies the CA-24 output gate to a failed check's text at
// the point where it enters a wake. github.PrepareWakeOutput is the canonical
// gate, but ADR 0005 keeps sibling infra packages apart, so the mcp package
// mirrors the failed-check branch here. Failed output is always inserted as a
// fragment bounded at failureExcerptLimit. The confirmation branch belongs to
// the wake path and never applies to get_result. Verdicts and check
// classification stay unchanged.
func prepareWakeExcerpt(output string) string {
	return clampExcerpt(output, failureExcerptLimit)
}

func cacheGet(ctx context.Context, c failureExcerptCache, watchID, headSHA, name string) (string, bool, error) {
	if c == nil {
		return "", false, nil
	}
	return c.Get(ctx, watchID, headSHA, name)
}

func cachePut(ctx context.Context, c failureExcerptCache, watchID, headSHA, name, excerpt string) error {
	if c == nil {
		return nil
	}
	return c.Put(ctx, watchID, headSHA, name, excerpt)
}

// escapeUntrusted escapes every character that could turn an annotation into
// markup, a JSON string delimiter or a control sequence. It mirrors the wake
// package's identifier escaping: annotations are untrusted CI output and must
// never be able to rewrite an agent's instructions or a tool parameter.
func escapeUntrusted(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(fmt.Sprintf("\\u%04x", r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// sanitizeExcerpt redacts secrets, escapes untrusted markup and bounds the
// text. Redaction runs on the raw bytes first so its byte patterns still match;
// escaping and truncation follow. A cut appends the truncation marker so the
// loss is visible. Truncation is rune-safe, so a multi-byte character is never
// cut in half.
func sanitizeExcerpt(raw string, limit int) string {
	if limit <= 0 {
		return ""
	}
	clean := observe.Redact([]byte(raw))
	return clampExcerpt(escapeUntrusted(string(clean)), limit)
}

// clampExcerpt clamps s to at most limit bytes. When it cuts text it appends
// the truncation marker, which stays in the cached string so a cache hit still
// shows the loss. It is rune-safe, so a multi-byte character is never cut in
// half.
func clampExcerpt(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	markerLen := len(failureExcerptTruncatedMarker)
	if limit <= markerLen {
		// No room for the marker; keep the plain rune-safe cut.
		cut := s[:limit]
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		return cut
	}
	cut := s[:limit-markerLen]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + failureExcerptTruncatedMarker
}

// clampRunes truncates s to at most limit characters. It is the character
// counterpart of clampExcerpt, which counts bytes. Thread history uses it
// because the T3.236 ceiling counts characters, not encoded bytes, so a
// multibyte message cannot consume more than its share of the page.
func clampRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// envFailureExcerptSource is the zero-wiring default. It reads the token from
// BEACON_GITHUB_TOKEN only: GitHub Actions injects GITHUB_TOKEN into every
// test run, and gating on that would make hermetic tests call the network. A
// nil source means "no credentials", which keeps the response unchanged.
// The API base can be overridden with BEACON_GITHUB_API for hermetic tests.
type envFailureExcerptSource struct {
	token  string
	base   string
	client *http.Client
}

func newEnvFailureExcerptSource() FailureExcerptSource {
	token := strings.TrimSpace(os.Getenv("BEACON_GITHUB_TOKEN"))
	if token == "" {
		return nil
	}
	base := strings.TrimSpace(os.Getenv("BEACON_GITHUB_API"))
	if base == "" {
		base = "https://api.github.com"
	}
	if !allowedExcerptBase(base) {
		return nil
	}
	return &envFailureExcerptSource{
		token:  token,
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// allowedExcerptBase reports whether base is a host we may send the GitHub
// token to. Only the public API over HTTPS and loopback for hermetic tests pass.
// A base that fails this check never becomes a source, so no request carries
// the token to an arbitrary host. Rejecting userinfo also rules out the
// "https://api.github.com@evil.example" form of host confusion.
func allowedExcerptBase(base string) bool {
	u, err := url.Parse(base)
	return err == nil && allowedExcerptURL(u)
}

// allowedExcerptURL is the URL form of allowedExcerptBase. getJSON calls it on
// the outgoing request so a directly constructed source cannot attach the Bearer
// token to a host that failed the check.
func allowedExcerptURL(u *url.URL) bool {
	if u == nil || u.Host == "" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.github.com":
		return u.Scheme == "https"
	case "localhost", "127.0.0.1", "::1":
		return u.Scheme == "http" || u.Scheme == "https"
	default:
		return false
	}
}

type githubCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

type githubAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Level     string `json:"annotation_level"`
}

// excerptCheckRunsURL builds the check-runs endpoint. owner, name and headSHA
// come from the Watch, so PathEscape keeps separators such as ".." inside one
// path segment instead of letting the caller rewrite the request path. The
// query uses url.Values so its encoding does not depend on string formatting.
func excerptCheckRunsURL(base, owner, name, headSHA string) string {
	q := url.Values{}
	q.Set("per_page", "100")
	return fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs?%s",
		base, url.PathEscape(owner), url.PathEscape(name), url.PathEscape(headSHA), q.Encode())
}

// excerptAnnotationsURL builds the annotations endpoint with the same path
// escaping as excerptCheckRunsURL. The run ID is numeric, so it needs no
// escaping; the query still goes through url.Values for one encoding path.
func excerptAnnotationsURL(base, owner, name string, id int64) string {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(failureExcerptAnnotationCount))
	return fmt.Sprintf("%s/repos/%s/%s/check-runs/%d/annotations?%s",
		base, url.PathEscape(owner), url.PathEscape(name), id, q.Encode())
}

// FailureExcerpt finds the failed CheckRun by name on the Watch's head SHA and
// reads its annotations through REST. The two endpoints are the REST half of
// W11-B; a GraphQL checkRun.annotations(first:N) source can be injected instead
// by supplying another FailureExcerptSource.
func (s *envFailureExcerptSource) FailureExcerpt(ctx context.Context, repo, headSHA, checkName string) (string, error) {
	// A missing field is a failed request, not a check without annotations.
	// Return an error so the caller can tell the two apart (EH-02).
	if s == nil || s.client == nil {
		return "", errors.New("github excerpt: source is not configured")
	}
	if repo == "" {
		return "", errors.New("github excerpt: missing repo")
	}
	if headSHA == "" {
		return "", errors.New("github excerpt: missing headSHA")
	}
	if checkName == "" {
		return "", errors.New("github excerpt: missing checkName")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("github excerpt: repo %q is not owner/name", repo)
	}
	path := excerptCheckRunsURL(s.base, owner, name, headSHA)
	var body struct {
		CheckRuns []githubCheckRun `json:"check_runs"`
	}
	if err := s.getJSON(ctx, path, &body); err != nil {
		return "", err
	}
	var id int64
	for _, run := range body.CheckRuns {
		if run.Name != checkName || !failedRunConclusion(run.Conclusion) {
			continue
		}
		id = run.ID
		break
	}
	if id == 0 {
		// No failed check run matches, so there are no annotations to read.
		// This is a successful empty answer, unlike the validation errors above.
		return "", nil
	}
	annotationsPath := excerptAnnotationsURL(s.base, owner, name, id)
	var annotations []githubAnnotation
	if err := s.getJSON(ctx, annotationsPath, &annotations); err != nil {
		return "", err
	}
	return formatAnnotations(annotations), nil
}

func (s *envFailureExcerptSource) getJSON(ctx context.Context, rawURL string, out any) error {
	u, err := url.Parse(rawURL)
	if err != nil || !allowedExcerptURL(u) {
		return errors.New("github excerpt: disallowed request host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github excerpt: status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// failedRunConclusion is the CheckRun conclusion subset W11-B treats as a
// failure. StatusContext states are capitalised and are not CheckRuns, so they
// are deliberately absent here as well.
func failedRunConclusion(conclusion string) bool {
	switch strings.ToUpper(conclusion) {
	case "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE", "CANCELLED", "CANCELED":
		return true
	}
	return false
}

// formatAnnotations renders at most failureExcerptAnnotationCount lines of
// "path:line: title: message". Empty fields are skipped so an annotation with
// only a message still produces a useful line. The caller bounds the result.
func formatAnnotations(annotations []githubAnnotation) string {
	var b strings.Builder
	for i, a := range annotations {
		if i >= failureExcerptAnnotationCount {
			break
		}
		msg := strings.TrimSpace(a.Message)
		title := strings.TrimSpace(a.Title)
		if msg == "" && title == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if p := strings.TrimSpace(a.Path); p != "" {
			b.WriteString(p)
			if a.StartLine > 0 {
				b.WriteByte(':')
				b.WriteString(strconv.Itoa(a.StartLine))
			}
			b.WriteString(": ")
		}
		if title != "" {
			b.WriteString(title)
			if msg != "" {
				b.WriteString(": ")
			}
		}
		b.WriteString(msg)
	}
	return b.String()
}

// --- Ship facts from observed checks (ADR 0040 §2, T3.75) -------------------
//
// Evaluate gates the ready verdict on the compiled-in ship formula, so a
// Snapshot assembled from a Watch must carry the facts the formula reads. The
// Watch carries observed checks and nothing else that could serve as an
// opinion: each fact turns on only for a required, green check with the
// matching role. A missing or failing check leaves its fact false, which is
// the honest value for a fact nobody observed.

// shipFactsFromWatch derives the ship facts from a Watch's CI checks. Check
// names are classified case-insensitively by substring so both "build" and
// "ci/build" (or "test" and "ci/tests") work. Trivial has no observable
// signal on a Watch, so it stays false rather than being invented.
func shipFactsFromWatch(w domain.Watch) domain.ShipFacts {
	var facts domain.ShipFacts
	for name, check := range w.Checks {
		if !check.Required || !isGreenCheck(check) {
			continue
		}
		if checkNameHas(name, "build") {
			facts.Build = true
		}
		if checkNameHas(name, "audit") {
			facts.Audit = true
		}
		if checkNameHas(name, "test") {
			facts.TDD = true
		}
	}
	return facts
}

// withShipFacts returns a copy of s whose Ship field is derived from w. Any
// caller that builds a Snapshot from a Watch before calling domain.Evaluate
// must go through this helper, otherwise the ready gate sees zero facts.
func withShipFacts(s domain.Snapshot, w domain.Watch) domain.Snapshot {
	s.Ship = shipFactsFromWatch(w)
	return s
}

// isGreenCheck reports whether c completed successfully. It is the green
// counterpart of isFailedCheck: a missing conclusion with a SUCCESS state,
// or a SUCCESS conclusion with a missing state, is still a green check.
func isGreenCheck(c domain.Check) bool {
	return strings.EqualFold(c.Conclusion, "SUCCESS") || strings.EqualFold(c.State, "SUCCESS")
}

// checkNameHas reports whether name contains want, ignoring case.
func checkNameHas(name, want string) bool {
	return strings.Contains(strings.ToLower(name), want)
}

// collectFailedChecks projects only the fields the contract allows. No check
// output, log text or raw payload is copied into the response.
func collectFailedChecks(w domain.Watch) []FailedCheck {
	out := make([]FailedCheck, 0, len(w.Checks))
	for name, check := range w.Checks {
		if !isFailedCheck(check) {
			continue
		}
		out = append(out, FailedCheck{
			Name:         name,
			Conclusion:   check.Conclusion,
			DetailsURL:   check.DetailsURL,
			Required:     check.Required,
			FlakyMatched: flakyMatched(name, w.Flaky.Names),
		})
	}
	return out
}

func isFailedCheck(c domain.Check) bool {
	if failureConclusions[c.Conclusion] {
		return true
	}
	// A cancelled check has no passing conclusion. The verdict side treats both
	// spellings as failures, so get_result must report them too (T3.402).
	switch c.State {
	case "FAILURE", "ERROR", "TIMED_OUT", "STARTUP_FAILURE", "CANCELLED", "CANCELED":
		return true
	}
	return false
}

// flakyMatched reports whether name is one of the configured flaky check names
// or matches one of its glob patterns (WORKER-PROMPT §4.6).
func flakyMatched(name string, names []string) bool {
	for _, pattern := range names {
		if pattern == name {
			return true
		}
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

// remainingRetryBudget is the smallest retry budget still available across the
// failed checks that matched the flaky set. A retry only happens when every
// failed check is within budget, so the minimum is the binding constraint. A
// watch with no flaky-matched failure has no retry budget.
func remainingRetryBudget(w domain.Watch, failed []FailedCheck) int {
	if len(failed) == 0 || w.Flaky.MaxRetries <= 0 {
		return 0
	}
	budget := 0
	set := false
	for _, fc := range failed {
		if !fc.FlakyMatched {
			continue
		}
		remaining := w.Flaky.MaxRetries - w.Flaky.Used[fc.Name]
		if remaining < 0 {
			remaining = 0
		}
		if !set || remaining < budget {
			budget = remaining
			set = true
		}
	}
	if !set {
		return 0
	}
	return budget
}

// mapResultReadError keeps a domain error unchanged and maps a store read
// failure to the contract code. The narrow read port cannot distinguish a
// missing row from an I/O failure, so a missing Watch or obligation is the
// documented interpretation of a read error here.
func mapResultReadError(err error, what string) error {
	var appErr *domain.AppError
	if errors.As(err, &appErr) {
		return err
	}
	return domain.NewAppErrorWithCause(domain.CodeWatchNotFound, what+": "+err.Error(), err)
}

// mapResultReadFailure maps a read error on the get_result, grade_result and
// grade_external_action paths. A domain error passes through unchanged. An
// empty selection keeps CodeWatchNotFound when the read port classifies it as
// missing. Any other read error becomes CodePersistFailed and keeps the cause
// in the message.
func mapResultReadFailure(store resultStoreReader, err error, what string) error {
	var appErr *domain.AppError
	if errors.As(err, &appErr) {
		return err
	}
	if classifier, ok := store.(resultStoreNotFound); ok && classifier.IsNotFound(err) {
		return domain.NewAppErrorWithCause(domain.CodeWatchNotFound, what+": "+err.Error(), err)
	}
	return domain.NewAppErrorWithCause(domain.CodePersistFailed, what+": "+err.Error(), err)
}

// --- Result rubric, separate from run completion (ADR 0034 §1) -------------
//
// A Watch reaching StatusClosed only says the run ended. Anthropic reports that
// a run can finish `completed` while the work on its streams failed, so run
// completion and result grading are two different states. The grader below is
// a pure function over Watch facts: it never sees the agent's own reasoning and
// performs no delivery stamp. Its ReadyVerdict is the only verdict allowed to
// mark a task done. The existing beacon_get_result projection is untouched.

// ResultOutcome is the grader's verdict on the work. The five values mirror the
// outcome-evaluation event from Anthropic, adopted in ADR 0034 §1.
type ResultOutcome string

const (
	OutcomeSatisfied            ResultOutcome = "satisfied"
	OutcomeNeedsRevision        ResultOutcome = "needs_revision"
	OutcomeMaxIterationsReached ResultOutcome = "max_iterations_reached"
	OutcomeFailed               ResultOutcome = "failed"
	OutcomeInterrupted          ResultOutcome = "interrupted"
	// OutcomeIndeterminate is the honest task result when an external action
	// was performed but its answer never came back (ADR 0039 §5). The effect
	// may already exist, so the core claims neither success nor failure and
	// starts no retry. It is the outcome of the task itself, not the delivery
	// ladder branch of the same name in ADR 0017 (domain.OutcomeIndeterminate).
	OutcomeIndeterminate ResultOutcome = "indeterminate"
)

// CriterionID names one verifiable criterion. Criteria are fixed before the
// run starts; forming them after the run would fit the rubric to the answer.
type CriterionID string

const (
	// CriterionRunFinished is the run-lifecycle fact, not a proof of work done.
	CriterionRunFinished CriterionID = "run_finished"
	// CriterionRequiredChecksOK fails when a required check has a failed
	// conclusion or failure state.
	CriterionRequiredChecksOK CriterionID = "required_checks_ok"
	// CriterionMergeReady fails on unresolved merge blockers.
	CriterionMergeReady CriterionID = "merge_ready"
	// CriterionVerdictReady checks the derived verdict computed by the domain
	// verdict engine, an observable fact rather than the agent's claim.
	CriterionVerdictReady CriterionID = "verdict_ready"
	// CriterionExternalActionResponse fails when an external action ran but
	// its answer never returned (ADR 0039 §5). The effect is then unknown, so
	// no other criterion can be claimed as proof of success or failure.
	CriterionExternalActionResponse CriterionID = "external_action_response"
)

const (
	defaultOutcomeIterations = 3
	maxOutcomeIterations     = 20
)

// CriterionEvaluation is one criterion result with an explanation. Explanations
// are returned on every outcome so the orchestrator can hand them to the agent
// on the next iteration (ADR 0034 §1).
type CriterionEvaluation struct {
	Criterion   CriterionID `json:"criterion"`
	Passed      bool        `json:"passed"`
	Explanation string      `json:"explanation"`
}

// ResultRubric is the set of verifiable criteria fixed before the run and the
// iteration budget. Criteria are IDs, never free-form claims, so the grader can
// run in its own context over the same facts.
type ResultRubric struct {
	Criteria      []CriterionID `json:"criteria"`
	MaxIterations int           `json:"maxIterations"`
}

// DefaultResultRubric returns the four built-in criteria and three iterations,
// the default budget from ADR 0034 §1.
func DefaultResultRubric() ResultRubric {
	return ResultRubric{
		Criteria: []CriterionID{
			CriterionRunFinished,
			CriterionRequiredChecksOK,
			CriterionMergeReady,
			CriterionVerdictReady,
		},
		MaxIterations: defaultOutcomeIterations,
	}
}

// RubricEvaluation is the separate result record stored next to the terminal
// verdict. ReadyVerdict is set only when Outcome is satisfied: a finished run
// alone never makes a verdict ready.
type RubricEvaluation struct {
	Outcome       ResultOutcome         `json:"outcome"`
	Iteration     int                   `json:"iteration"`
	MaxIterations int                   `json:"maxIterations"`
	Criteria      []CriterionEvaluation `json:"criteria"`
	ReadyVerdict  domain.Verdict        `json:"readyVerdict,omitempty"`
	// LeaseKey is the durable obligation key of the caller that owns an
	// external action whose response was lost (ADR 0039 §5). It is set only for
	// an indeterminate result so the caller can correlate the possible effect
	// without starting a retry. Other outcomes leave it empty and keep their
	// previous bytes.
	LeaseKey string `json:"leaseKey,omitempty"`
	// NeedsHumanDecision is set only when the iteration budget ran out. It
	// tells the caller to park the Watch and wait for a human to continue the
	// run. Every other outcome leaves it false (CV-09).
	NeedsHumanDecision bool `json:"needsHumanDecision,omitempty"`
}

// gradeResult grades w against rubric. It reads no clock, does no I/O and never
// mutates its arguments, which makes the grader independent of the agent and
// verifiable in CI. iteration is the number of evaluations already performed;
// interrupted reports a caller-driven preemption, which the run cannot claim
// as its own end_turn.
func gradeResult(w domain.Watch, rubric ResultRubric, iteration int, interrupted bool) RubricEvaluation {
	maxIterations := rubric.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultOutcomeIterations
	}
	if maxIterations > maxOutcomeIterations {
		maxIterations = maxOutcomeIterations
	}
	if iteration < 0 {
		iteration = 0
	}

	ev := RubricEvaluation{
		Iteration:     iteration,
		MaxIterations: maxIterations,
		Criteria:      make([]CriterionEvaluation, 0, len(rubric.Criteria)),
	}
	allPassed := len(rubric.Criteria) > 0
	runFinished := false
	for _, id := range rubric.Criteria {
		ce := evaluateCriterion(w, id)
		if id == CriterionRunFinished && ce.Passed {
			runFinished = true
		}
		if !ce.Passed {
			allPassed = false
		}
		ev.Criteria = append(ev.Criteria, ce)
	}

	switch {
	case interrupted:
		ev.Outcome = OutcomeInterrupted
	case allPassed:
		ev.Outcome = OutcomeSatisfied
		ev.ReadyVerdict = w.Verdict
	case iteration >= maxIterations:
		ev.Outcome = OutcomeMaxIterationsReached
		ev.NeedsHumanDecision = true
	case runFinished:
		// The run ended yet at least one criterion failed: completed is not
		// the same as done, and there is no run left to revise.
		ev.Outcome = OutcomeFailed
	default:
		ev.Outcome = OutcomeNeedsRevision
	}
	return ev
}

// gradeExternalAction grades a task whose execution touched the outside world.
// performed says the action reached that world; responseLost says its answer
// never returned. Only both together produce indeterminate: the effect may
// already have happened, so the core cannot claim success or failure, cannot
// set a ready verdict and cannot justify a retry (ADR 0039 §5). The durable lease
// key travels with the outcome so the caller can correlate the unknown effect.
// Every other combination falls through to the ordinary rubric grading.
func gradeExternalAction(w domain.Watch, rubric ResultRubric, iteration int, performed, responseLost bool, leaseKey string) RubricEvaluation {
	if !performed || !responseLost {
		return gradeResult(w, rubric, iteration, false)
	}

	maxIterations := rubric.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultOutcomeIterations
	}
	if maxIterations > maxOutcomeIterations {
		maxIterations = maxOutcomeIterations
	}
	if iteration < 0 {
		iteration = 0
	}
	return RubricEvaluation{
		Outcome:       OutcomeIndeterminate,
		Iteration:     iteration,
		MaxIterations: maxIterations,
		Criteria: []CriterionEvaluation{{
			Criterion:   CriterionExternalActionResponse,
			Passed:      false,
			Explanation: "external action was performed but its response was lost; effect unknown",
		}},
		LeaseKey: leaseKey,
	}
}

// evaluateCriterion turns Watch facts into one explained criterion result.
func evaluateCriterion(w domain.Watch, id CriterionID) CriterionEvaluation {
	switch id {
	case CriterionRunFinished:
		if w.Status == domain.StatusClosed || !w.ClosedAt.IsZero() {
			return passedCriterion(id, "run reached a terminal state")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "run has not reached a terminal state; status=" + string(w.Status)}
	case CriterionRequiredChecksOK:
		failed := failedRequiredCheckNames(w)
		if len(failed) == 0 {
			return passedCriterion(id, "no required check failed")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "required checks failed: " + strings.Join(failed, ", ")}
	case CriterionMergeReady:
		status := strings.ToUpper(w.Merge.MergeStateStatus)
		if strings.ToUpper(w.Merge.Mergeable) == "MERGEABLE" && status != "DIRTY" && status != "BEHIND" {
			return passedCriterion(id, "merge is clean")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: fmt.Sprintf("mergeable=%s mergeStateStatus=%s", w.Merge.Mergeable, w.Merge.MergeStateStatus)}
	case CriterionVerdictReady:
		if w.Verdict == domain.VerdictReady {
			return passedCriterion(id, "verdict is ready")
		}
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "verdict=" + string(w.Verdict)}
	default:
		return CriterionEvaluation{Criterion: id, Passed: false,
			Explanation: "unknown criterion"}
	}
}

func passedCriterion(id CriterionID, why string) CriterionEvaluation {
	return CriterionEvaluation{Criterion: id, Passed: true, Explanation: why}
}

// failedRequiredCheckNames lists, in stable order, the required checks whose
// conclusion or state counts as a failure (WORKER-PROMPT §4.6).
func failedRequiredCheckNames(w domain.Watch) []string {
	names := make([]string, 0)
	for name, check := range w.Checks {
		if check.Required && isFailedCheck(check) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// resultWatchWriter is the optional write side used to park a Watch whose
// iteration budget ran out. A composition root that wants parking supplies it;
// a read-only caller leaves the Watch untouched.
type resultWatchWriter interface {
	SaveWatch(domain.Watch) error
}

// parkForInput moves w to needs_input with the domain transition and saves the
// result through the optional write side. The transition is idempotent for an
// already parked Watch and never creates a successor Watch (CV-09).
func (h *GetResultHandler) parkForInput(w domain.Watch) (domain.Watch, error) {
	next, effects := domain.Transition(w, domain.NeedsInputEvent(h.clock.Now()))
	for _, effect := range effects {
		if effect.Kind == domain.EffectCreateWatch {
			return w, domain.NewAppError(domain.CodeInvalidInput, "grade_result: parking must not create a new Watch")
		}
	}
	writer, ok := h.store.(resultWatchWriter)
	if !ok {
		return next, nil
	}
	if err := writer.SaveWatch(next); err != nil {
		return w, domain.NewAppErrorWithCause(domain.CodePersistFailed, err.Error(), err)
	}
	return next, nil
}

// GradeResult is the separate result check. It re-reads the Watch from the
// store and grades it against rubric, so its verdict does not depend on how the
// agent reported the run. An exhausted budget parks the Watch for a human
// answer with the domain needs_input event. Grading stamps no delivery fact:
// readAt/claimedAt stay owned by the notification path (ADR 0014).
func (h *GetResultHandler) GradeResult(ctx context.Context, watchID string, rubric ResultRubric, iteration int, interrupted bool) (RubricEvaluation, error) {
	_ = ctx
	if watchID == "" {
		return RubricEvaluation{}, domain.NewAppError(domain.CodeInvalidInput, "grade_result: watchID is required")
	}
	if h == nil || h.store == nil {
		return RubricEvaluation{}, domain.NewAppError(domain.CodePersistFailed, "grade_result: store is nil")
	}
	w, err := h.store.Watch(watchID)
	if err != nil {
		return RubricEvaluation{}, mapResultReadFailure(h.store, err, "grade_result: watch")
	}
	ev := gradeResult(w, rubric, iteration, interrupted)
	if ev.NeedsHumanDecision {
		if _, err := h.parkForInput(w); err != nil {
			return RubricEvaluation{}, err
		}
	}
	return ev, nil
}

// GradeExternalAction is the ADR 0039 §5 entry point. It grades a task after an
// external action and reports indeterminate when that action ran but its answer
// was lost. leaseKey is the durable obligation key from get_result; it is
// returned with the indeterminate outcome and nowhere else. Grading reads the
// Watch only and stamps no delivery fact: a delivery-ladder indeterminate
// attempt in ADR 0017 never turns into a task result here.
func (h *GetResultHandler) GradeExternalAction(ctx context.Context, watchID string, rubric ResultRubric, iteration int, performed, responseLost bool, leaseKey string) (RubricEvaluation, error) {
	_ = ctx
	if watchID == "" {
		return RubricEvaluation{}, domain.NewAppError(domain.CodeInvalidInput, "grade_external_action: watchID is required")
	}
	if h == nil || h.store == nil {
		return RubricEvaluation{}, domain.NewAppError(domain.CodePersistFailed, "grade_external_action: store is nil")
	}
	w, err := h.store.Watch(watchID)
	if err != nil {
		return RubricEvaluation{}, mapResultReadFailure(h.store, err, "grade_external_action: watch")
	}
	return gradeExternalAction(w, rubric, iteration, performed, responseLost, leaseKey), nil
}

// AttachGetResult replaces the startup stub with the real tool. Wiring it into
// NewServer is a separate integration task, so this helper is opt-in.
func AttachGetResult(server *mcpsdk.Server, handler *GetResultHandler) {
	if server == nil || handler == nil {
		return
	}
	server.RemoveTools("beacon_get_result")
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "beacon_get_result",
		Description: "Read a Watch result without logs and mark its content as read.",
		Annotations: attachToolAnnotations(true),
	}, handler.Handle)
}

// --- Background task handle: wait, output, notify (ADR 0048 feature 3) -----
//
// A background task handle answers three questions that the mechanisms already
// in this file do not. The signed opaque handle of T3.96 proves a name was
// issued by Beacon, but says nothing about waiting or reading output. The
// --after threshold of T3.103 (SetAfter/ApplyTaskForm above) turns a slow
// synchronous call into a task form, but it is a single server-wide time limit,
// not a per-call wait. This section fixes only the wait/output/notify contract:
//
//   - wait(timeout) reports how long it actually waited and whether it timed
//     out; the timeout is supplied by the caller on every call, so no 30/120s
//     threshold is duplicated here;
//   - output(offset) reads the task's output from a byte offset, so a caller can
//     stream bytes without restarting from zero;
//   - completion is a single broadcast notification every waiter can observe.
//
// The handle here is a plain identifier. Signing or verifying it is T3.96's job
// and is deliberately not repeated.

// taskClock separates time from the wait so the contract is testable without
// real sleeps. The production value delegates to wall time; tests drive it.
type taskClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type wallTaskClock struct{}

func (wallTaskClock) Now() time.Time                         { return time.Now() }
func (wallTaskClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// TaskCompletion is the one notification published when a background task
// finishes. It carries the handle and a caller-supplied terminal status. Output
// bytes are never placed in the notification; they are read with Output.
type TaskCompletion struct {
	Handle   string `json:"handle"`
	Terminal string `json:"terminal,omitempty"`
}

// TaskWaitResult is the answer of Wait. WaitedMs is the observed wait time in
// milliseconds. TimedOut is true only when the caller's timeout elapsed before
// the task completed. Done is true when the task had finished by the time Wait
// returned.
type TaskWaitResult struct {
	WaitedMs int64 `json:"waitedMs"`
	TimedOut bool  `json:"timedOut"`
	Done     bool  `json:"done"`
}

// TaskOutputChunk is the answer of Output. Data holds the output bytes starting
// at the requested offset. NextOffset is the byte offset to pass on the next
// read, so repeated reads continue where the previous one stopped.
type TaskOutputChunk struct {
	Data       []byte `json:"data"`
	NextOffset int    `json:"nextOffset"`
}

// maxBackgroundTaskOutputBytes bounds the bytes retained for one task. A chunk
// that crosses the bound keeps the head and tail and replaces the middle with
// an explicit marker, so the end of a log is never lost.
const maxBackgroundTaskOutputBytes = 64 * 1024

// backgroundOutputTruncatedMark is inserted where the middle of a bounded
// output was dropped. It belongs to the retained stream and to NextOffset.
const backgroundOutputTruncatedMark = "\n[beacon: output truncated]\n"

// BackgroundTask is one long-running task. Its output is bounded, it can be
// waited on up to a caller timeout, and its completion is broadcast once
// through Done. The zero value is unusable; build one with NewBackgroundTask.
type BackgroundTask struct {
	handle string
	clock  taskClock

	mu         sync.Mutex
	output     []byte
	baseOffset int
	done       bool
	terminal   string
	doneCh     chan struct{}
	onComplete func(string)
}

// NewBackgroundTask builds a task named by handle. A nil clock falls back to
// wall time. The handle is stored as an opaque name; no signature is computed
// or checked here (T3.96 owns that).
func NewBackgroundTask(handle string, clock taskClock) *BackgroundTask {
	if clock == nil {
		clock = wallTaskClock{}
	}
	return &BackgroundTask{handle: handle, clock: clock, doneCh: make(chan struct{})}
}

// Handle returns the opaque name of the task.
func (t *BackgroundTask) Handle() string { return t.handle }

// AppendOutput adds chunk to the task output. Bytes appended after completion
// are kept too, so a late tail is never silently lost. Once the output crosses
// maxBackgroundTaskOutputBytes the middle is replaced by a marker while the
// head and tail survive.
func (t *BackgroundTask) AppendOutput(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, chunk...)
	if len(t.output) <= maxBackgroundTaskOutputBytes {
		return
	}
	oldEnd := t.baseOffset + len(t.output)
	mark := []byte(backgroundOutputTruncatedMark)
	head := (maxBackgroundTaskOutputBytes - len(mark)) / 2
	tail := maxBackgroundTaskOutputBytes - len(mark) - head
	kept := make([]byte, 0, maxBackgroundTaskOutputBytes)
	kept = append(kept, t.output[:head]...)
	kept = append(kept, mark...)
	kept = append(kept, t.output[len(t.output)-tail:]...)
	t.output = kept
	// Renumber the retained window so its last byte keeps the offset it had
	// before truncation. A reader that keeps up never sees NextOffset go back.
	t.baseOffset = oldEnd - len(kept)
}

// Output returns the output bytes from offset. A negative offset reads from the
// start; an offset past the end yields an empty chunk whose NextOffset is the
// current end, so a caller that lost its position can resynchronise. A reader
// behind the retained window also starts at its first byte. The returned slice
// is a copy: a caller cannot mutate the task's stored bytes.
func (t *BackgroundTask) Output(offset int) TaskOutputChunk {
	t.mu.Lock()
	defer t.mu.Unlock()
	start := offset - t.baseOffset
	if start < 0 {
		start = 0
	}
	if start > len(t.output) {
		start = len(t.output)
	}
	data := append([]byte(nil), t.output[start:]...)
	return TaskOutputChunk{Data: data, NextOffset: t.baseOffset + start + len(data)}
}

// Complete marks the task finished with terminal and closes the broadcast
// channel exactly once. Later calls are no-ops, so a duplicated completion
// cannot close the channel twice or replace the first terminal status. A task
// started through TaskRegistry leaves the registry here; terminal carries the
// reason, including cancellation.
func (t *BackgroundTask) Complete(terminal string) {
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	t.done = true
	t.terminal = terminal
	close(t.doneCh)
	onComplete := t.onComplete
	t.mu.Unlock()
	if onComplete != nil {
		onComplete(t.handle)
	}
}

// Done returns the channel closed by Complete. Selecting on it receives the
// completion notification without polling; every waiter observes the same
// close.
func (t *BackgroundTask) Done() <-chan struct{} { return t.doneCh }

// Completion returns the notification published by Complete and whether the
// task has finished. Reading it does not consume it, so every waiter can read
// the same value.
func (t *BackgroundTask) Completion() (TaskCompletion, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		return TaskCompletion{}, false
	}
	return TaskCompletion{Handle: t.handle, Terminal: t.terminal}, true
}

// Wait blocks until the task completes, the caller's timeout elapses, or ctx is
// cancelled. A non-positive timeout checks completion once and returns at once.
// The timeout is a per-call value from the caller, not the server sync-call
// threshold of T3.103.
func (t *BackgroundTask) Wait(ctx context.Context, timeout time.Duration) TaskWaitResult {
	if timeout < 0 {
		timeout = 0
	}
	start := t.clock.Now()
	if _, done := t.Completion(); done {
		return TaskWaitResult{WaitedMs: 0, Done: true}
	}
	if timeout == 0 {
		return TaskWaitResult{WaitedMs: 0, TimedOut: true}
	}
	timer := t.clock.After(timeout)
	select {
	case <-t.doneCh:
		return TaskWaitResult{WaitedMs: t.clock.Now().Sub(start).Milliseconds(), Done: true}
	case <-timer:
		done := t.isDone()
		return TaskWaitResult{WaitedMs: timeout.Milliseconds(), TimedOut: !done, Done: done}
	case <-ctx.Done():
		return TaskWaitResult{WaitedMs: t.clock.Now().Sub(start).Milliseconds()}
	}
}

func (t *BackgroundTask) isDone() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// TaskRegistry addresses background tasks by their opaque handle. It is the set
// of tasks a caller can wait on, read from and be notified about. Handle
// verification stays in T3.96 and is intentionally not repeated here.
type TaskRegistry struct {
	mu    sync.Mutex
	clock taskClock
	tasks map[string]*BackgroundTask
}

// NewTaskRegistry builds a registry. A nil clock falls back to wall time.
func NewTaskRegistry(clock taskClock) *TaskRegistry {
	if clock == nil {
		clock = wallTaskClock{}
	}
	return &TaskRegistry{clock: clock, tasks: map[string]*BackgroundTask{}}
}

// Start registers a new task under handle and returns it. An empty handle or a
// duplicate is rejected, so a handle always names exactly one task.
func (r *TaskRegistry) Start(handle string) (*BackgroundTask, error) {
	if handle == "" {
		return nil, fmt.Errorf("task: handle is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[handle]; exists {
		return nil, fmt.Errorf("task: handle already started: %q", handle)
	}
	t := NewBackgroundTask(handle, r.clock)
	t.onComplete = r.remove
	r.tasks[handle] = t
	return t, nil
}

// Get resolves a handle to its task. The bool reports whether the handle is
// registered. A finished task is no longer registered, so the bool is false.
func (r *TaskRegistry) Get(handle string) (*BackgroundTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[handle]
	return t, ok
}

// remove unregisters a finished task. Complete calls it without holding the
// task lock, so the two locks are never taken in the same order.
func (r *TaskRegistry) remove(handle string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, handle)
}

// --- MCP protocol tasks/get contract with TTL and notifications (ADR 0056 f1) ---
//
// This section is the protocol-level contract of the io.modelcontextprotocol/
// tasks extension (SEP-2663, schema/2026-07-28). It is deliberately NOT the
// internal background-task handle above. T3.113's BackgroundTask and
// TaskRegistry decide how Beacon stores and streams one Watch result; this
// section fixes what a client sees on the wire:
//
//   - starting long work returns a task_id immediately, so the caller does not
//     hold a connection open while the work runs (CreateTaskResult);
//   - the caller retrieves status and result later with tasks/get;
//   - every task carries a TTL after which it is gone;
//   - progress and result changes are published as notifications/tasks.
//
// Names and status values are the schema's, with no local variants (ADR 0030 §1).
// Beacon keeps its own storage, work queue and event delivery behind this
// contract; only the wire shape lives here (ADR 0056 feature 1).

// TasksGetMethod is the JSON-RPC method of the contract.
const TasksGetMethod = "tasks/get"

// taskStatusNotificationMethod is the notifications/tasks method of the
// extension.
const taskStatusNotificationMethod = "notifications/tasks"

// taskResultTypeComplete is the tasks/get response discriminator. The create
// discriminator reuses TaskResultType ("task") from schema.go.
const taskResultTypeComplete = "complete"

// TaskState is the status of a task. The five values are the schema values,
// never local spellings (ADR 0030 §1).
type TaskState string

const (
	TaskStateWorking       TaskState = "working"
	TaskStateInputRequired TaskState = "input_required"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCancelled     TaskState = "cancelled"
)

// Terminal reports whether s is a final state. A terminal task is never
// reopened: a later event cannot replace its result (ADR 0030 §3).
func (s TaskState) Terminal() bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCancelled:
		return true
	default:
		return false
	}
}

// validTaskState reports whether s is one of the schema's five statuses.
func validTaskState(s TaskState) bool {
	switch s {
	case TaskStateWorking, TaskStateInputRequired, TaskStateCompleted, TaskStateFailed, TaskStateCancelled:
		return true
	default:
		return false
	}
}

// NotificationKind classifies a task status as a progress update or a final
// result. It is derived from status; the wire notification has no local kind
// field (ADR 0030 §1).
type NotificationKind string

const (
	NotificationProgress NotificationKind = "progress"
	NotificationResult   NotificationKind = "result"
)

func (s TaskState) NotificationKind() NotificationKind {
	if s.Terminal() {
		return NotificationResult
	}
	return NotificationProgress
}

// ProtocolTask is the Task object of the extension. Field names, timestamp
// format and status values follow schema/2026-07-28 exactly. Only fields
// allowed for the current status are populated: result for completed, error
// for failed, inputRequests for input_required.
type ProtocolTask struct {
	TaskID         string          `json:"taskId"`
	Status         TaskState       `json:"status"`
	StatusMessage  string          `json:"statusMessage,omitempty"`
	CreatedAt      string          `json:"createdAt"`
	LastUpdatedAt  string          `json:"lastUpdatedAt"`
	TTLMs          *int64          `json:"ttlMs"`
	PollIntervalMs int64           `json:"pollIntervalMs,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          json.RawMessage `json:"error,omitempty"`
	InputRequests  json.RawMessage `json:"inputRequests,omitempty"`
}

// TaskResult is the flat wire envelope shared by CreateTaskResult
// (resultType "task") and GetTaskResult (resultType "complete"). The embedded
// Task fields are flattened into the object, as the schema requires.
type TaskResult struct {
	ProtocolTask
	ResultType string `json:"resultType"`
}

func createTaskResult(t ProtocolTask) TaskResult {
	return TaskResult{ProtocolTask: t, ResultType: TaskResultType}
}

func getTaskResult(t ProtocolTask) TaskResult {
	return TaskResult{ProtocolTask: t, ResultType: taskResultTypeComplete}
}

// TaskStatusNotification is the notifications/tasks notification. Its params
// carry the complete Task for the current status, so progress (working /
// input_required) and result (completed / failed / cancelled) are told apart
// by params.status rather than by an extra field.
type TaskStatusNotification struct {
	Method string       `json:"method"`
	Params ProtocolTask `json:"params"`
}

// TaskPatch is the status change applied by ProtocolTaskStore.Update. Only
// the field belonging to Status is stored; the others are ignored.
type TaskPatch struct {
	Status        TaskState
	StatusMessage string
	Result        json.RawMessage
	Error         json.RawMessage
	InputRequests json.RawMessage
}

// protocolTaskRecord keeps the wire task next to its creation time, so TTL can
// be checked without reparsing the RFC3339 string on every read.
type protocolTaskRecord struct {
	task      ProtocolTask
	createdAt time.Time
}

// maxTaskStatusNotifications bounds the notification ring. Only the newest
// statuses stay readable; the current state still comes from Get, so an
// evicted note cannot change it.
const maxTaskStatusNotifications = 256

// ProtocolTaskStore is Beacon's own store for the protocol contract. A clock
// drives creation timestamps, last-update timestamps and TTL expiry. The work
// queue and event delivery stay outside this contract (ADR 0056 feature 1).
type ProtocolTaskStore struct {
	mu     sync.Mutex
	clock  domain.Clock
	tasks  map[string]protocolTaskRecord
	notes  []TaskStatusNotification
	nextID uint64
}

// NewProtocolTaskStore builds a store. A nil clock falls back to wall time.
func NewProtocolTaskStore(clock domain.Clock) *ProtocolTaskStore {
	if clock == nil {
		clock = getResultWallClock{}
	}
	return &ProtocolTaskStore{clock: clock, tasks: map[string]protocolTaskRecord{}, nextID: 1}
}

// Start creates a task and returns its CreateTaskResult immediately. It does
// not wait for completion, poll or hold a connection: the caller receives the
// taskId at once and is free to disconnect. A non-positive ttlMs means an
// unlimited task and is emitted as null, per schema.
func (s *ProtocolTaskStore) Start(ttlMs, pollIntervalMs int64) (TaskResult, error) {
	if s == nil {
		return TaskResult{}, domain.NewAppError(domain.CodePersistFailed, "tasks: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now().UTC()
	var ttl *int64
	if ttlMs > 0 {
		v := ttlMs
		ttl = &v
	}
	t := ProtocolTask{
		TaskID:         fmt.Sprintf("task-%d", s.nextID),
		Status:         TaskStateWorking,
		CreatedAt:      now.Format(time.RFC3339Nano),
		LastUpdatedAt:  now.Format(time.RFC3339Nano),
		TTLMs:          ttl,
		PollIntervalMs: pollIntervalMs,
	}
	s.nextID++
	s.tasks[t.TaskID] = protocolTaskRecord{task: t, createdAt: now}
	return createTaskResult(t), nil
}

// Get implements tasks/get. It returns the current DetailedTask for taskId
// with resultType "complete". A task whose TTL elapsed is gone (CodeGone); an
// id that was never issued is CodeWatchNotFound. Reading does not change the
// task or publish a notification.
func (s *ProtocolTaskStore) Get(taskID string) (TaskResult, error) {
	if s == nil || s.clock == nil {
		return TaskResult{}, domain.NewAppError(domain.CodePersistFailed, "tasks/get: store is nil")
	}
	if taskID == "" {
		return TaskResult{}, domain.NewAppError(domain.CodeInvalidInput, "tasks/get: taskId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return TaskResult{}, domain.NewAppError(domain.CodeWatchNotFound, "tasks/get: task not found")
	}
	if rec.task.TTLMs != nil {
		expires := rec.createdAt.Add(time.Duration(*rec.task.TTLMs) * time.Millisecond)
		if !s.clock.Now().Before(expires) {
			// The first expired read still reports CodeGone. The record is
			// dropped here so a long-lived store does not keep one task per
			// expired id; a later read reports CodeWatchNotFound.
			delete(s.tasks, taskID)
			return TaskResult{}, domain.NewAppError(domain.CodeGone, "tasks/get: task TTL elapsed")
		}
	}
	return getTaskResult(rec.task), nil
}

// Update applies patch to a non-terminal task and publishes a
// notifications/tasks notification. The new state (including its result) is
// stored before the notification is recorded, so a late event can never
// overwrite a terminal result (ADR 0030 §3). Updating a terminal task is
// rejected instead of reopening it.
func (s *ProtocolTaskStore) Update(taskID string, patch TaskPatch) (ProtocolTask, error) {
	if s == nil || s.clock == nil {
		return ProtocolTask{}, domain.NewAppError(domain.CodePersistFailed, "tasks: store is nil")
	}
	if taskID == "" || !validTaskState(patch.Status) {
		return ProtocolTask{}, domain.NewAppError(domain.CodeInvalidInput, "tasks: taskId and a valid status are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tasks[taskID]
	if !ok {
		return ProtocolTask{}, domain.NewAppError(domain.CodeWatchNotFound, "tasks: task not found")
	}
	if rec.task.Status.Terminal() {
		return ProtocolTask{}, domain.NewAppError(domain.CodeInvalidInput, "tasks: terminal task cannot be updated")
	}
	t := rec.task
	t.Status = patch.Status
	t.StatusMessage = patch.StatusMessage
	t.LastUpdatedAt = s.clock.Now().UTC().Format(time.RFC3339Nano)
	t.Result = nil
	t.Error = nil
	t.InputRequests = nil
	switch patch.Status {
	case TaskStateCompleted:
		t.Result = patch.Result
	case TaskStateFailed:
		t.Error = patch.Error
	case TaskStateInputRequired:
		t.InputRequests = patch.InputRequests
	}
	rec.task = t
	s.tasks[taskID] = rec
	s.publishLocked(t)
	return t, nil
}

// Cancel cooperatively moves a task to the cancelled state with a reason. It
// is a result notification, like any other terminal update.
func (s *ProtocolTaskStore) Cancel(taskID, reason string) (ProtocolTask, error) {
	return s.Update(taskID, TaskPatch{Status: TaskStateCancelled, StatusMessage: reason})
}

// publishLocked records a notification in a bounded ring. Callers hold s.mu.
func (s *ProtocolTaskStore) publishLocked(t ProtocolTask) {
	n := TaskStatusNotification{Method: taskStatusNotificationMethod, Params: t}
	if len(s.notes) >= maxTaskStatusNotifications {
		copy(s.notes, s.notes[1:])
		s.notes[len(s.notes)-1] = n
		return
	}
	s.notes = append(s.notes, n)
}

// Notifications returns the published notifications in order. The slice is a
// copy, so a caller cannot mutate the store's history.
func (s *ProtocolTaskStore) Notifications() []TaskStatusNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TaskStatusNotification(nil), s.notes...)
}

// GetTaskInput is the argument object of the tasks/get tool.
type GetTaskInput struct {
	TaskID string `json:"taskId" jsonschema:"identifier of the task to query"`
}

// GetHandle adapts tasks/get to the go-sdk typed tool handler signature.
// Domain errors are returned as-is for ToErrorShape to convert at the MCP
// boundary.
func (s *ProtocolTaskStore) GetHandle(ctx context.Context, req *mcpsdk.CallToolRequest, in GetTaskInput) (*mcpsdk.CallToolResult, TaskResult, error) {
	_ = ctx
	_ = req
	out, err := s.Get(in.TaskID)
	if err != nil {
		return nil, TaskResult{}, err
	}
	return nil, out, nil
}

// AttachTasksGet registers tasks/get as an opt-in tool. Wiring it into
// NewServer is a separate integration task, so this helper is not called
// automatically.
func AttachTasksGet(server *mcpsdk.Server, store *ProtocolTaskStore) {
	if server == nil || store == nil {
		return
	}
	server.RemoveTools(TasksGetMethod)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        TasksGetMethod,
		Description: "Query the state and result of an MCP task without holding a connection.",
		Annotations: attachToolAnnotations(true),
	}, store.GetHandle)
}
