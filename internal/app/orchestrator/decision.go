package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// DecisionKind is one of the mutually exclusive outcomes of the orchestrator's
// merge decision. The kinds are spelled out so callers cannot confuse a held
// merge with a failed one.
type DecisionKind string

const (
	// DecisionMerge lets the caller merge the Watch revision.
	DecisionMerge DecisionKind = "MERGE"
	// DecisionWaitDependency holds the merge until one or more waitFor refs
	// land in main. Refs lists the ones still missing.
	DecisionWaitDependency DecisionKind = "WAIT_DEPENDENCY"
	// DecisionDependencyNotFound reports a waitFor ref that names a missing
	// task card. Waiting cannot create the card, so the decision asks the
	// orchestrator to fix the ref instead of parking the Watch forever.
	DecisionDependencyNotFound DecisionKind = "DEPENDENCY_NOT_FOUND"
	// DecisionBlockedFixAgent hands a blocked Watch to the FIX agent. Reason
	// and FailedChecks come from Watch.Blocked.
	DecisionBlockedFixAgent DecisionKind = "BLOCKED_FIX_AGENT"
	// DecisionRetryExecutor sends a retry back to the executor that owns the
	// Watch, because the failure is in its own PR.
	DecisionRetryExecutor DecisionKind = "RETRY_EXECUTOR"
	// DecisionNotifyOrchestrator covers everything else: no merge, no retry
	// and no dependency to wait for. A human or the orchestrator decides.
	DecisionNotifyOrchestrator DecisionKind = "NOTIFY_ORCHESTRATOR"
	// DecisionRejectGeneratedEdit refuses a Watch whose diff edits a generated
	// artifact. The gate fails the card: no merge, no retry and no wake.
	DecisionRejectGeneratedEdit DecisionKind = "REJECT_GENERATED_EDIT"
	// DecisionSourceError means the rules source could not be resolved, so the
	// check could not run. It fails closed: no merge, no retry and no wake.
	DecisionSourceError DecisionKind = "SOURCE_ERROR"
)

const (
	// MaxFixAttempts caps how many FIX agents may be spawned for one blocked
	// Watch. The cap is per block: an unblock clears the block record and the
	// next block starts from zero.
	MaxFixAttempts = 3
	// ReasonEscalateHuman is the NOTIFY_ORCHESTRATOR reason returned once the
	// fix-attempt cap is reached. The orchestrator gets an escalation ping
	// instead of a request to spawn another FIX agent.
	ReasonEscalateHuman = "escalate_human"
	// ReasonGeneratedFileEdited is the REJECT_GENERATED_EDIT reason. It names
	// the invariant, not the file; the offending paths are in Decision.GeneratedFiles.
	ReasonGeneratedFileEdited = "generated_file_edited"
	// ReasonSourceUnavailable is the SOURCE_ERROR reason. The resolver error
	// is kept in Decision.SourceError.
	ReasonSourceUnavailable = "source_unavailable"
	// ReasonDependencyCardNotFound is the DEPENDENCY_NOT_FOUND reason. The
	// broken refs are kept in Decision.Refs.
	ReasonDependencyCardNotFound = "dependency_card_not_found"
)

// DefaultGeneratedFilePatterns lists the Beacon artifacts that tooling owns.
// These are the Beacon analogues of Harbor's generated demo GIF and golden
// baseline (ADR 0228): a workflow regenerates them, and nobody hand-edits them
// to turn a red test green.
//
//   - docs/ROADMAP_STATUS.md is rendered from task-card state;
//   - docs/tasks/*.md cards are written by batch-apply from the queue;
//   - evals/reports/* reports are emitted by the eval runner.
//
// Decide always uses this list, so a caller cannot silently opt out. A gate
// that needs its own list can pass one to GeneratedFileEdits instead.
//
// Gate contract (W12-C owns the gate adapter): read the PR's changed paths,
// call GeneratedFileEdits(files, DefaultGeneratedFilePatterns), and fail the
// card with REJECT_GENERATED_EDIT when the result is non-empty. Regenerate the
// artifact from its source instead of editing it by hand.
var DefaultGeneratedFilePatterns = []string{
	"docs/ROADMAP_STATUS.md",
	"docs/tasks/*.md",
	"evals/reports/*",
}

// BlockedInfo is why an executor blocked a Watch. It mirrors the domain block
// details without importing domain types into this package (ADR 0005).
// FixAttempts counts FIX agents already spawned for this block. It is stored
// on the block record so clearing the block on unblock also resets it.
type BlockedInfo struct {
	Reason       string   `json:"reason"`
	FailedChecks []string `json:"failedChecks,omitempty"`
	BlockedBy    string   `json:"blockedBy,omitempty"`
	FixAttempts  int      `json:"fixAttempts,omitempty"`
}

// DependencyResolverFunc adapts a function to the DependencyResolver contract
// declared in orchestrator.go. Decide shares that contract with canAutoMerge,
// so one resolver serves both paths.
type DependencyResolverFunc func(ctx context.Context, ref string) (bool, error)

// Satisfied calls f. A nil function reports the ref as unsatisfied.
func (f DependencyResolverFunc) Satisfied(ctx context.Context, ref string) (bool, error) {
	if f == nil {
		return false, nil
	}
	return f(ctx, ref)
}

// CIState is the CI view that accompanies a Watch. Verdict is consulted only
// when the Watch carries no verdict of its own: an explicit Watch verdict
// stays authoritative so a delayed CI poll cannot reclassify a blocked Watch
// as ready.
type CIState struct {
	Verdict string `json:"verdict,omitempty"`
}

// Decision is the single outcome of one orchestrator pass for one Watch.
// WatchID, PR and Revision travel with every outcome, so the caller can key
// merges, wakes and retries on (watchID, revision) and stay idempotent.
type Decision struct {
	Kind         DecisionKind `json:"kind"`
	WatchID      string       `json:"watchID"`
	PR           int          `json:"pr"`
	Revision     uint64       `json:"revision"`
	Refs         []string     `json:"refs,omitempty"`
	Reason       string       `json:"reason,omitempty"`
	BlockedBy    string       `json:"blockedBy,omitempty"`
	FailedChecks []string     `json:"failedChecks,omitempty"`
	// GeneratedFiles lists the changed paths that matched a generated-file
	// pattern. It is set only on DecisionRejectGeneratedEdit.
	GeneratedFiles []string `json:"generatedFiles,omitempty"`
	// ByRef reports whether the rules check ran against a git ref. It is set
	// on every decision that reached the check, so a caller can tell a
	// ref-backed result from one made against the PR diff.
	ByRef bool `json:"byRef,omitempty"`
	// SourceRef names the git ref the check ran against. It is empty for a
	// diff-backed check and lets a caller reproduce the result on the same ref.
	SourceRef string `json:"sourceRef,omitempty"`
	// SourceError carries the resolver error when Kind is SOURCE_ERROR.
	SourceError string `json:"sourceError,omitempty"`
}

// Source is the port a rules check reads its candidate files from. The gate
// supports two modes (ADR 0304): check against changed files or against a git
// ref. Both reduce to a list of repo-relative paths, so the pure model never
// opens a working copy. Turning a ref into paths is the adapter's job.
type Source interface {
	// ChangedFiles returns the repo-relative paths to check. An error means the
	// source could not be resolved and the caller must fail closed.
	ChangedFiles(ctx context.Context) ([]string, error)
	// Ref is the git ref backing this source, or "" for a diff source. A
	// non-empty value marks the resulting decision as ref-backed.
	Ref() string
}

// FilesSource is a Source backed by an explicit changed-file list. Its Ref is
// empty, so a check made from it is not marked as ref-backed.
type FilesSource struct {
	Files []string
}

// ChangedFiles returns the file list unchanged.
func (s FilesSource) ChangedFiles(context.Context) ([]string, error) {
	return s.Files, nil
}

// Ref is empty for a diff-backed source.
func (FilesSource) Ref() string { return "" }

// RefSource is a Source backed by a git ref. Resolve supplies the paths changed
// at that ref. The model passes the ref to Resolve and never touches a working
// copy, so the same ref always produces the same check.
type RefSource struct {
	// RefName names the git ref to check. An empty name makes the source
	// diff-like: it has no ref, resolves to no files and does not mark the
	// decision as ref-backed.
	RefName string
	// Resolve returns the paths changed at RefName. A nil resolver is an error.
	Resolve func(ctx context.Context, ref string) ([]string, error)
}

// ChangedFiles resolves the ref into its changed paths. An empty ref resolves
// to no files without calling the resolver.
func (s RefSource) ChangedFiles(ctx context.Context) ([]string, error) {
	ref := strings.TrimSpace(s.RefName)
	if ref == "" {
		return nil, nil
	}
	if s.Resolve == nil {
		return nil, fmt.Errorf("orchestrator: ref source %q has no resolver", ref)
	}
	return s.Resolve(ctx, ref)
}

// Ref returns the trimmed git ref, or "" when no ref was set.
func (s RefSource) Ref() string { return strings.TrimSpace(s.RefName) }

// CheckSource resolves src into the candidate file list and reports whether the
// check is ref-backed. The flag comes from Source.Ref, so a source with an
// empty ref is treated as diff-backed. A resolver error is returned with a nil
// file list; the caller must fail closed instead of reading the list as an
// empty diff.
func CheckSource(ctx context.Context, src Source) (files []string, byRef bool, err error) {
	if src == nil {
		return nil, false, nil
	}
	ref := strings.TrimSpace(src.Ref())
	files, err = src.ChangedFiles(ctx)
	if err != nil {
		return nil, ref != "", fmt.Errorf("orchestrator: rules source: %w", err)
	}
	return files, ref != "", nil
}

// Decide reduces a Watch, its dependency resolver and the CI view to exactly
// one Decision. It has no side effects: the caller owns the merge ledger and
// the wake delivery. Priority is fixed and does not depend on field order:
//
//  1. a blocked Watch goes to the FIX agent, even if its verdict still reads
//     ready or a dependency is outstanding. Once MaxFixAttempts FIX agents
//     have been spawned for that block, the Watch is escalated to a human
//     with NOTIFY_ORCHESTRATOR instead of asking for another FIX agent;
//  2. a file list that edits a generated artifact is rejected before any
//     dependency is resolved, so a manual edit can never reach the merge path;
//     the list comes from Watch.Source, which may be a git ref, and a source
//     error fails closed as SOURCE_ERROR;
//  3. a waitFor/waitForMain ref that names a missing task card is an incident
//     and is reported with its ref, because waiting cannot create the card;
//  4. other unsatisfied refs hold the merge and list the refs;
//  5. ready plus auto-merge merges;
//  6. retry goes back to the executor;
//  7. anything else is escalated, never merged.
func Decide(w Watch, deps DependencyResolver, ci CIState) Decision {
	d := Decision{WatchID: w.ID, PR: w.PR, Revision: w.Revision}

	if w.Blocked != nil {
		d.BlockedBy = w.Blocked.BlockedBy
		d.FailedChecks = append([]string(nil), w.Blocked.FailedChecks...)
		if w.Blocked.FixAttempts >= MaxFixAttempts {
			d.Kind = DecisionNotifyOrchestrator
			d.Reason = ReasonEscalateHuman
			return d
		}
		d.Kind = DecisionBlockedFixAgent
		d.Reason = w.Blocked.Reason
		return d
	}

	src := w.Source
	if src == nil {
		src = FilesSource{Files: w.ChangedFiles}
	}
	files, byRef, srcErr := CheckSource(context.Background(), src)
	d.ByRef = byRef
	if byRef {
		d.SourceRef = strings.TrimSpace(src.Ref())
	}
	if srcErr != nil {
		d.Kind = DecisionSourceError
		d.Reason = ReasonSourceUnavailable
		d.SourceError = srcErr.Error()
		return d
	}

	if edited := GeneratedFileEdits(files, DefaultGeneratedFilePatterns); len(edited) > 0 {
		d.Kind = DecisionRejectGeneratedEdit
		d.Reason = ReasonGeneratedFileEdited
		d.GeneratedFiles = edited
		return d
	}

	waiting, notFound := unsatisfiedRefs(w, deps)
	if len(notFound) > 0 {
		d.Kind = DecisionDependencyNotFound
		d.Refs = notFound
		d.Reason = ReasonDependencyCardNotFound
		return d
	}
	if len(waiting) > 0 {
		d.Kind = DecisionWaitDependency
		d.Refs = waiting
		return d
	}

	verdict := decisionVerdict(w, ci)
	switch {
	case verdict == VerdictReady && decisionAutoMergeAllowed(w.Policy):
		d.Kind = DecisionMerge
		d.Reason = VerdictReady
	case verdict == VerdictRetry:
		d.Kind = DecisionRetryExecutor
		d.Reason = verdict
	default:
		d.Kind = DecisionNotifyOrchestrator
		d.Reason = decisionHoldReason(verdict, w.Policy)
	}
	return d
}

// GeneratedFileEdits returns the changed paths that match one of patterns,
// in first-seen order. It is pure: inputs are never mutated and the result is
// a fresh slice, so the caller can attach it to a Decision or a gate failure
// without aliasing the diff. An empty result means the diff is safe to hand to
// the gate; an empty changedFiles list is always safe.
//
// A pattern is a repo-relative path with optional `*` wildcards. Exact paths
// match themselves; `docs/tasks/*.md` covers the cards in that directory and
// a trailing `/*` also covers nested paths (for example `evals/reports/2026/x.md`
// matches `evals/reports/*`). Backslashes are normalised to `/` and a leading
// `./` is dropped so the gate can pass paths from either OS.
func GeneratedFileEdits(changedFiles, patterns []string) []string {
	var out []string
	seen := make(map[string]bool, len(changedFiles))
	for _, raw := range changedFiles {
		file := normalizeGeneratedPath(raw)
		if file == "" || seen[file] {
			continue
		}
		for _, rawPattern := range patterns {
			pattern := normalizeGeneratedPath(rawPattern)
			if pattern != "" && matchesGeneratedPath(pattern, file) {
				seen[file] = true
				out = append(out, file)
				break
			}
		}
	}
	return out
}

func normalizeGeneratedPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, "/")
}

func matchesGeneratedPath(pattern, file string) bool {
	if pattern == file {
		return true
	}
	if ok, err := path.Match(pattern, file); err == nil && ok {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		dir := strings.TrimSuffix(pattern, "/*")
		if dir != "" && strings.HasPrefix(file, dir+"/") {
			return true
		}
	}
	return false
}

// unsatisfiedRefs resolves waitFor first and waitForMain second, preserving
// order and dropping duplicates. Ordinary resolution errors count as waiting,
// so an unverifiable fact never allows a merge. A *DependencyCardNotFoundError
// goes into notFound: the ref cannot ever resolve, so Decide reports an
// incident instead of parking the Watch in WAIT_DEPENDENCY.
func unsatisfiedRefs(w Watch, deps DependencyResolver) (waiting, notFound []string) {
	if w.Policy == nil {
		return nil, nil
	}
	seen := make(map[string]bool)
	for _, group := range [][]string{w.Policy.WaitFor, w.Policy.WaitForMain} {
		for _, raw := range group {
			ref := strings.TrimSpace(raw)
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			if deps == nil {
				waiting = append(waiting, ref)
				continue
			}
			satisfied, err := deps.Satisfied(context.Background(), ref)
			var missing *DependencyCardNotFoundError
			if errors.As(err, &missing) {
				notFound = append(notFound, ref)
				continue
			}
			if err != nil || !satisfied {
				waiting = append(waiting, ref)
			}
		}
	}
	return waiting, notFound
}

// decisionVerdict lowercases and trims the Watch verdict, falling back to CI
// only when the Watch is silent. A blocked Watch is already handled above, so
// this fallback cannot resurrect a stale ready verdict into a merge.
func decisionVerdict(w Watch, ci CIState) string {
	if v := strings.ToLower(strings.TrimSpace(w.Verdict)); v != "" {
		return v
	}
	return strings.ToLower(strings.TrimSpace(ci.Verdict))
}

// decisionAutoMergeAllowed checks only the explicit hold-backs. WaitFor and
// WaitForMain are not treated as permanent denials here: they have just been
// resolved above, and a satisfied dependency must not block the merge.
func decisionAutoMergeAllowed(p *AutoMergePolicy) bool {
	if p == nil {
		return true
	}
	if p.AutoMerge != nil && !*p.AutoMerge {
		return false
	}
	return !p.RequireApproval
}

func decisionHoldReason(verdict string, p *AutoMergePolicy) string {
	if p != nil {
		if p.AutoMerge != nil && !*p.AutoMerge {
			return "auto_merge_disabled"
		}
		if p.RequireApproval {
			return "require_approval"
		}
	}
	if verdict != "" {
		return "verdict=" + verdict
	}
	return "verdict="
}

// ---- HD-019 / ADR 0295: task completion audit ----

// TaskClosure answers whether one task named in a plan has been closed. It is
// the only way AuditCompletion learns the result, so the audit itself performs
// no I/O. An implementation must return an error instead of true when the fact
// cannot be verified. An unverifiable task must never count as closed.
type TaskClosure interface {
	Closed(ctx context.Context, id string) (bool, error)
}

// TaskClosureFunc adapts a function to the TaskClosure contract. A nil
// function reports every task as open, so an unwired audit fails closed.
type TaskClosureFunc func(ctx context.Context, id string) (bool, error)

// Closed calls f. A nil function reports the task as open.
func (f TaskClosureFunc) Closed(ctx context.Context, id string) (bool, error) {
	if f == nil {
		return false, nil
	}
	return f(ctx, id)
}

// CompletionAudit is the outcome of reconciling a plan with its task result.
// Complete is true only when every task in the plan is closed and the closure
// port reported no error. Missing lists the plan tasks that are still open or
// could not be verified, in plan order. Partial completion is never complete.
type CompletionAudit struct {
	Complete bool     `json:"complete"`
	Missing  []string `json:"missing,omitempty"`
}

// AuditCompletion reconciles a plan with the result by task list. It is pure:
// the closure port is the sole source of task state, so the audit opens no
// files, reads no cards and contacts no hosting service by itself. Plan
// entries are trimmed and deduplicated in first-seen order; an empty plan is
// complete. A nil closure treats every task as open. A closure error is
// returned with an incomplete audit: a task whose state cannot be verified
// must never unblock a card.
func AuditCompletion(ctx context.Context, plan []string, closure TaskClosure) (CompletionAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := CompletionAudit{Missing: []string{}}
	seen := make(map[string]bool, len(plan))
	for _, raw := range plan {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		closed := false
		if closure != nil {
			var err error
			closed, err = closure.Closed(ctx, id)
			if err != nil {
				out.Missing = append(out.Missing, id)
				return out, fmt.Errorf("orchestrator: task closure %q: %w", id, err)
			}
		}
		if !closed {
			out.Missing = append(out.Missing, id)
		}
	}
	out.Complete = len(out.Missing) == 0
	return out, nil
}

// ---- RS-CMP-02: one signal = one spawn ----

// SignalReportToOrchestrator is the only completion signal that opens an
// execution slot. It is the nextAction carried by the terminal_ack that ends a
// wave item. Other signals never open a slot.
const SignalReportToOrchestrator = "report_to_orchestrator"

// SlotArtifact is the proof a spawn leaves behind before its token burns. A
// plan-mode task leaves PLAN.md; a full-mode task leaves a PR. Either field may
// be set, but at least one must be non-empty for the artifact to count.
type SlotArtifact struct {
	Plan string `json:"plan,omitempty"`
	PR   int    `json:"pr,omitempty"`
}

// Valid reports whether the artifact names a concrete completion proof.
func (a SlotArtifact) Valid() bool {
	return strings.TrimSpace(a.Plan) != "" || a.PR > 0
}

// SlotToken is a one-shot permission to open one execution slot for one
// completion signal. Its key is (WatchID, Revision), the same idempotency key
// merges and wakes use, so two signals from one task at one revision cannot
// open two slots.
type SlotToken struct {
	WatchID  string `json:"watchID"`
	Revision uint64 `json:"revision"`
	PR       int    `json:"pr,omitempty"`
	Signal   string `json:"signal"`
}

// Key returns the idempotency key for the token. WatchID and Revision alone
// identify one signal, so PR and Signal do not take part in the key.
func (t SlotToken) Key() string {
	return fmt.Sprintf("%s@%d", t.WatchID, t.Revision)
}

// SlotIncidentKind names a mismatch between a completion signal and a spawn.
type SlotIncidentKind string

const (
	// SlotDuplicateSignal fires when a second completion signal arrives while
	// the first signal's token is still live. No second slot opens.
	SlotDuplicateSignal SlotIncidentKind = "duplicate_signal"
	// SlotSpawnWithoutToken fires when a spawn reports an artifact but no live
	// token covers the signal. The signal and the spawn do not match.
	SlotSpawnWithoutToken SlotIncidentKind = "spawn_without_token"
)

// SlotIncident is a pure incident value. The caller records it in observe and
// fails the card. No I/O happens here, so the rule stays testable without a
// store, a hosting service or a wake adapter.
type SlotIncident struct {
	Kind     SlotIncidentKind `json:"kind"`
	WatchID  string           `json:"watchID"`
	Revision uint64           `json:"revision"`
	Detail   string           `json:"detail,omitempty"`
}

// ArtifactVerifier reports whether artifact is present for token. A nil
// verifier verifies nothing, so the token stays live and no slot frees. The
// wiring layer owns the real PLAN.md read and PR lookup; neither lives in this
// package.
type ArtifactVerifier interface {
	Verified(ctx context.Context, token SlotToken, artifact SlotArtifact) (bool, error)
}

// ArtifactVerifierFunc adapts a function to ArtifactVerifier.
type ArtifactVerifierFunc func(ctx context.Context, token SlotToken, artifact SlotArtifact) (bool, error)

// Verified calls f. A nil function reports the artifact as not verified.
func (f ArtifactVerifierFunc) Verified(ctx context.Context, t SlotToken, a SlotArtifact) (bool, error) {
	if f == nil {
		return false, nil
	}
	return f(ctx, t, a)
}

// SlotLedger enforces one signal = one spawn. It is pure: the caller owns
// persistence, the wake delivery and the observe write. A live token means a
// spawn is authorized and its artifact is not verified yet.
type SlotLedger struct {
	live map[string]SlotToken
}

// NewSlotLedger returns an empty ledger.
func NewSlotLedger() *SlotLedger {
	return &SlotLedger{live: map[string]SlotToken{}}
}

// Issue handles one completion signal. Only SignalReportToOrchestrator opens
// a slot. A second signal for the same (watchID, revision) returns a
// SlotDuplicateSignal incident and no token, so the caller cannot open a
// second slot.
func (l *SlotLedger) Issue(signal string, watchID string, revision uint64, pr int) (SlotToken, *SlotIncident) {
	if signal != SignalReportToOrchestrator {
		return SlotToken{}, nil
	}
	t := SlotToken{WatchID: watchID, Revision: revision, PR: pr, Signal: signal}
	if _, ok := l.live[t.Key()]; ok {
		return SlotToken{}, &SlotIncident{
			Kind:     SlotDuplicateSignal,
			WatchID:  watchID,
			Revision: revision,
			Detail:   "slot token already live for " + t.Key(),
		}
	}
	if l.live == nil {
		l.live = map[string]SlotToken{}
	}
	l.live[t.Key()] = t
	return t, nil
}

// Burn extinguishes token after its artifact is verified. A verifier that
// reports false, including one whose answer is delayed, leaves the token live
// and returns false so the slot stays held and no new signal can open another
// one. A burn for a token that is not live is a SlotSpawnWithoutToken
// incident: a spawn claims an artifact with no matching signal.
func (l *SlotLedger) Burn(ctx context.Context, token SlotToken, artifact SlotArtifact, verifier ArtifactVerifier) (bool, *SlotIncident) {
	if _, ok := l.live[token.Key()]; !ok {
		return false, &SlotIncident{
			Kind:     SlotSpawnWithoutToken,
			WatchID:  token.WatchID,
			Revision: token.Revision,
			Detail:   "no live slot token for " + token.Key(),
		}
	}
	if !artifact.Valid() || verifier == nil {
		return false, nil
	}
	verified, err := verifier.Verified(ctx, token, artifact)
	if err != nil || !verified {
		return false, nil
	}
	delete(l.live, token.Key())
	return true, nil
}

// IsLive reports whether token still holds its slot.
func (l *SlotLedger) IsLive(watchID string, revision uint64) bool {
	key := SlotToken{WatchID: watchID, Revision: revision}.Key()
	_, ok := l.live[key]
	return ok
}
