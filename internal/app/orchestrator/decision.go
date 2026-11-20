package orchestrator

import (
	"context"
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
//  3. unsatisfied waitFor/waitForMain refs hold the merge and list the refs;
//  4. ready plus auto-merge merges;
//  5. retry goes back to the executor;
//  6. anything else is escalated, never merged.
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

	if refs := unsatisfiedRefs(w, deps); len(refs) > 0 {
		d.Kind = DecisionWaitDependency
		d.Refs = refs
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
// order and dropping duplicates. Resolution errors count as unsatisfied.
func unsatisfiedRefs(w Watch, deps DependencyResolver) []string {
	if w.Policy == nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, group := range [][]string{w.Policy.WaitFor, w.Policy.WaitForMain} {
		for _, raw := range group {
			ref := strings.TrimSpace(raw)
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			satisfied := false
			if deps != nil {
				satisfied, _ = deps.Satisfied(context.Background(), ref)
			}
			if !satisfied {
				out = append(out, ref)
			}
		}
	}
	return out
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
