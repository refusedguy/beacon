// Package orchestrator implements the automatic merge loop over active Beacon
// Watches. The core is pure over five narrow ports: Store reads active
// Watches, remembers completed merges and closes Watches; Merger performs the
// hosting-service merge; DependencyResolver resolves free-form waitFor and
// waitForMain references; Waker leaves a retry/fail/wait Watch for its
// subscriber; Source supplies the file list a rules check reads, from either a
// PR diff or a git ref. Concrete adapters live in this package and in cmd/beacon.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	// ARDH-15: observe is the documented cross-cutting exception. Metrics are
	// emitted at the decision sites; a port through every call would add noise
	// without moving an adapter.
	"github.com/refusedguy/beacon/internal/infra/ghci"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// The verdict strings below mirror internal/domain/types.go. ADR 0005 forbids
// app from importing domain, so a test in this package checks the values
// against domain.Verdicts().
const (
	// VerdictReady is the only verdict that permits a merge.
	VerdictReady = "ready"
	// VerdictWait is the in-process hold decision when a dependency is not
	// satisfied. It carries a reason in the Action recorded for the Watch.
	VerdictWait = "wait"
	// VerdictWaitDependency is the persisted hold status. It is written to the
	// Watch so beacon_list_running and beacon inspect can show why a merge is
	// held, and so the next tick re-checks the dependency instead of dropping
	// the Watch. The reason follows the colon in the persisted verdict.
	VerdictWaitDependency = "wait_dependency"
	// VerdictRetry and VerdictFail are left for the subscriber's wake. Wake
	// delivery belongs to another card. VerdictFail is an orchestrator-internal
	// decision, not a domain.Verdict: the bridge never writes it to the store,
	// so verdict_published_total never carries a fail label.
	VerdictRetry = "retry"
	VerdictFail  = "fail"
)

// OrchestratorVerdicts returns the verdict strings the merge loop can emit.
func OrchestratorVerdicts() []string {
	return []string{VerdictReady, VerdictWait, VerdictWaitDependency, VerdictRetry, VerdictFail}
}

// Action kinds recorded in Result.Actions.
const (
	ActionMerge       = "merge"
	ActionSkip        = "skip"
	ActionDryRun      = "dry-run"
	ActionNeedsRebase = "needs-rebase"
	ActionWake        = "wake"
	ActionWait        = "wait"
	// ActionBlockedFixAgent records one FIX agent request for a blocked Watch.
	ActionBlockedFixAgent = "blocked-fix-agent"
	// ActionBlockEscalated records a blocked Watch that exhausted its FIX budget.
	ActionBlockEscalated = "block-escalated"
	// ActionApprovalFirst is the approval request for the original attempt,
	// which runs with a narrow set of extra permissions.
	ActionApprovalFirst = "approval-first"
	// ActionApprovalRetry is the approval request for the retry after a
	// sandbox denial. It runs escalated and carries no extra permissions.
	ActionApprovalRetry = "approval-retry"
)

// AutoMergePolicy is the autor-merge policy of one Watch. AutoMerge is a
// pointer so the absent value means "default true": a Watch with no declared
// policy is mergeable. Any of the three hold-backs disables automatic merging.
//
// Allows is the conservative check without a resolver: a non-empty waitFor or
// waitForMain blocks there. The resolver-aware decision is Watch.canAutoMerge,
// which allows the merge once every reference is actually satisfied.
type AutoMergePolicy struct {
	AutoMerge       *bool    `json:"autoMerge,omitempty"`
	RequireApproval bool     `json:"requireApproval"`
	WaitFor         []string `json:"waitFor,omitempty"`
	WaitForMain     []string `json:"waitForMain,omitempty"`
}

// DefaultAutoMergePolicy is the zero-configuration policy: automatic merging
// is on and no hold-back is set.
func DefaultAutoMergePolicy() AutoMergePolicy {
	return AutoMergePolicy{}
}

// Allows reports whether this policy permits an automatic merge without
// resolving dependencies. It stays conservative so a caller without a
// DependencyResolver can never merge a Watch with unresolved references.
func (p AutoMergePolicy) Allows() bool {
	if p.AutoMerge != nil && !*p.AutoMerge {
		return false
	}
	return !p.RequireApproval && len(p.WaitFor) == 0 && len(p.WaitForMain) == 0
}

// DependencyResolver answers whether one free-form dependency reference is
// satisfied. A ref may name a task card (T3.123), a pull request (#12, PR#12,
// pr/12) or a file/card path that has to exist on main.
//
// An implementation must return an error instead of true when the fact cannot
// be verified. An unverifiable dependency must never allow a merge.
type DependencyResolver interface {
	Satisfied(ctx context.Context, ref string) (bool, error)
}

// Watch is the subset of a Beacon Watch the loop decides on. A nil Policy
// means the default autor-merge policy applies.
type Watch struct {
	ID       string
	Repo     string
	PR       int
	HeadSHA  string
	Verdict  string
	Revision uint64
	Policy   *AutoMergePolicy
	// Blocked carries the reason the executor blocked this Watch. Decide reads
	// it first so a stale ready verdict can never turn into a merge.
	Blocked *BlockedInfo
	// ChangedFiles lists repo-relative paths touched by the PR diff. The gate
	// adapter fills it from the PR; Decide checks it against
	// DefaultGeneratedFilePatterns so a hand-edited generated artifact can
	// never reach the merge path. A nil or empty list means no diff is known.
	ChangedFiles []string
	// Source supplies the file list the rules check reads. A nil Source falls
	// back to ChangedFiles, so callers that only know the PR diff keep working.
	// Set Source to a RefSource to check a git ref without a working copy
	// (ADR 0304).
	Source Source
}

func (w Watch) autoMergeAllowed() bool {
	if w.Policy == nil {
		return DefaultAutoMergePolicy().Allows()
	}
	return w.Policy.Allows()
}

func (w Watch) isReady() bool {
	return strings.EqualFold(strings.TrimSpace(w.Verdict), VerdictReady)
}

// canAutoMerge is the resolver-aware merge gate. It returns VerdictReady when
// the merge is allowed, VerdictWait with a reason when a dependency holds it,
// and an empty verdict for an explicit static hold-back (autoMerge=false or
// requireApproval), which needs no wake. A resolver error never turns into a
// ready verdict: it is returned to the caller and the Watch stays put.
func (w Watch) canAutoMerge(ctx context.Context, resolver DependencyResolver) (verdict string, reason string, err error) {
	policy := DefaultAutoMergePolicy()
	if w.Policy != nil {
		policy = *w.Policy
	}
	if policy.AutoMerge != nil && !*policy.AutoMerge {
		return "", "auto_merge_disabled", nil
	}
	if policy.RequireApproval {
		return "", "require_approval", nil
	}
	if len(policy.WaitFor) == 0 && len(policy.WaitForMain) == 0 {
		return VerdictReady, "", nil
	}
	if resolver == nil {
		return VerdictWait, "resolver_unavailable", nil
	}
	for _, ref := range policy.WaitFor {
		ok, err := resolver.Satisfied(ctx, ref)
		if err != nil {
			return VerdictWait, "resolve:" + ref, fmt.Errorf("orchestrator: resolve waitFor %q: %w", ref, err)
		}
		if !ok {
			return VerdictWait, "wait_for:" + ref, nil
		}
	}
	for _, ref := range policy.WaitForMain {
		ok, err := resolver.Satisfied(ctx, ref)
		if err != nil {
			return VerdictWait, "resolve_main:" + ref, fmt.Errorf("orchestrator: resolve waitForMain %q: %w", ref, err)
		}
		if !ok {
			return VerdictWait, "wait_for_main:" + ref, nil
		}
	}
	return VerdictReady, "", nil
}

// WaitDependencyVerdict builds the persisted status for one unsatisfied ref.
// Keeping the reason in the verdict makes it visible in the diagnostics that
// read Watch rows directly, without changing their projection.
func WaitDependencyVerdict(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return VerdictWaitDependency
	}
	return VerdictWaitDependency + ":" + reason
}

// isWaitDependency reports whether a persisted verdict is a dependency hold.
// Such a Watch is re-checked on the next pass: when the ref lands in main the
// same path that held the merge now merges it.
func isWaitDependency(verdict string) bool {
	v := strings.ToLower(strings.TrimSpace(verdict))
	return v == VerdictWaitDependency || strings.HasPrefix(v, VerdictWaitDependency+":")
}

// needsWake reports whether the verdict is one a subscriber must act on. The
// orchestrator does not fix these Watches; it only reports their PRs and, when
// a Waker is installed, hands the Watch over.
func needsWake(verdict string) bool {
	v := strings.ToLower(strings.TrimSpace(verdict))
	return v == VerdictRetry || v == VerdictFail
}

// Merger merges one pull request. repo names the owning repository; an
// adapter may use it to select the host but is not required to (the gh CLI
// infers the repository from the working directory).
type Merger interface {
	Merge(ctx context.Context, repo string, pr int) error
}

// MergeOutcome is the result of a checked merge. Merged is true only after the
// hosting service confirmed state=MERGED, so it is the sole permission to write
// the ledger and close the Watch. Pending means the merge command succeeded but
// the state is not observable yet: the Watch stays active and no ledger row is
// written. Reason is a short diagnostic for a PR the gate refused to merge.
type MergeOutcome struct {
	Merged  bool
	Pending bool
	Reason  string
}

// CheckedMerger is an alternate merge port for adapters that own the gh gate
// and confirmation. A checked adapter reads PR state before deciding to merge
// and after the merge command returns, then reports one of the three outcomes
// above. A caller passes it explicitly in Options.CheckedMerger; the two merge
// guarantees are never mixed by a type assertion on Merger.
type CheckedMerger interface {
	MergeChecked(ctx context.Context, repo string, pr int) (MergeOutcome, error)
}

// Store is the surface the loop needs from Beacon state: active Watches, the
// durable (watchID, revision) merge ledger and Watch cancellation.
type Store interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
	WasMerged(ctx context.Context, watchID string, revision uint64) (bool, error)
	RecordMerge(ctx context.Context, watchID string, revision uint64) error
	CloseWatch(ctx context.Context, watchID string) error
	// RecordFixAttempt persists one more FIX agent attempt on the Watch's
	// block record. The counter belongs to the block, so an unblock resets it.
	RecordFixAttempt(ctx context.Context, watchID string) error
}

// WatchCloseConflict reports that a Watch changed after the merge decision.
// The close is rejected, so the caller must not rewrite the newer record.
type WatchCloseConflict struct {
	WatchID string
	// Revision and HeadSHA are the values the close expected.
	Revision uint64
	HeadSHA  string
	// GotRevision and GotHeadSHA are the values found in the store.
	GotRevision uint64
	GotHeadSHA  string
	// GotStatus is the status found in the store. A head change closes the
	// Watch with its own reason, so a late merge must not replace that reason.
	GotStatus string
}

func (e *WatchCloseConflict) Error() string {
	return fmt.Sprintf("orchestrator: watch %s close conflict: want revision %d head %s, got revision %d head %s status %s",
		e.WatchID, e.Revision, e.HeadSHA, e.GotRevision, e.GotHeadSHA, e.GotStatus)
}

// RevisionedWatchCloser is an optional Store capability. A store that
// implements it closes a Watch only while the row still matches the revision,
// head and status the merge decision saw. A store without it keeps the plain
// CloseWatch path.
type RevisionedWatchCloser interface {
	CloseWatchAt(ctx context.Context, watchID string, revision uint64, headSHA string) error
}

// WatchHolder is an optional Store capability for persisting a dependency
// hold. A store without it still holds the merge in memory for this pass; the
// reason just is not visible in beacon_list_running / beacon inspect.
type WatchHolder interface {
	HoldWatch(ctx context.Context, watchID, verdict string) error
}

// Waker asks a subscriber to act on a retry/fail/wait Watch. Wake delivery
// lands in another card; a nil Waker leaves those Watches untouched apart from
// being reported in Result.NeedsRebase and Result.Waiting.
type Waker interface {
	Wake(ctx context.Context, watchID string, revision uint64, verdict string) error
}

// ReasonWaker is an alternate wake port for decisions that need to explain
// themselves. A wait holds on a named dependency, so the subscriber needs the
// reason, not just the verdict. A caller passes it explicitly in
// Options.ReasonWaker; the plain Waker receives only the verdict.
type ReasonWaker interface {
	WakeWithReason(ctx context.Context, watchID string, revision uint64, verdict, reason string) error
}

// Action is one decision the loop made, recorded for output and diagnostics.
// Message carries the hold reason for a wait Action.
type Action struct {
	Action   string `json:"action"`
	WatchID  string `json:"watchID"`
	PR       int    `json:"pr"`
	Revision uint64 `json:"revision"`
	Command  string `json:"command,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ApprovalPermissions are the sandbox permissions an approval would grant for
// one attempt. Escalations counts RequireEscalated grants; Extra lists any
// additional permissions. An escalated retry carries one escalation and no
// Extra, so approval grants exactly what the retry actually runs with.
type ApprovalPermissions struct {
	Escalations int      `json:"escalations,omitempty"`
	Extra       []string `json:"extra,omitempty"`
}

// ApprovalAction is an approval request bound to one Watch attempt. Keeping it
// separate from Action lets a caller present the escalated retry for approval
// in place of the first attempt, which runs with narrower permissions.
type ApprovalAction struct {
	Action      string              `json:"action"`
	WatchID     string              `json:"watchID"`
	PR          int                 `json:"pr"`
	Revision    uint64              `json:"revision"`
	Permissions ApprovalPermissions `json:"permissions"`
}

// ApprovalActions returns the first-attempt and escalated-retry approval
// requests for one Watch. The first keeps the caller's narrow Extra
// permissions and asks for no escalation. After a sandbox denial the retry is
// rewritten with RequireEscalated and the extra permissions are dropped, so
// its approval carries one escalation and an empty Extra list
// (codex-rs/core/src/tools/orchestrator.rs:521-532). The same place in Codex
// decides whether the retry needs approval at all (:496-506). Beacon had only
// the single RequireApproval boolean; these two actions make the distinction
// without widening the MCP tool contract.
func ApprovalActions(w Watch, narrow []string) (first, retry ApprovalAction) {
	first = ApprovalAction{
		Action:   ActionApprovalFirst,
		WatchID:  w.ID,
		PR:       w.PR,
		Revision: w.Revision,
		Permissions: ApprovalPermissions{
			Extra: append([]string(nil), narrow...),
		},
	}
	retry = ApprovalAction{
		Action:   ActionApprovalRetry,
		WatchID:  w.ID,
		PR:       w.PR,
		Revision: w.Revision,
		Permissions: ApprovalPermissions{
			Escalations: 1,
		},
	}
	return first, retry
}

// Result is the outcome of one orchestrate pass. NeedsRebase lists the PRs of
// retry/fail Watches left for their subscribers; Waiting lists PRs held by an
// unsatisfied dependency.
type Result struct {
	DryRun      bool     `json:"dryRun"`
	Merged      []int    `json:"merged"`
	Skipped     []int    `json:"skipped,omitempty"`
	Waiting     []int    `json:"waiting"`
	NeedsRebase []int    `json:"needsRebase"`
	Actions     []Action `json:"actions"`
	// Approvals carries the approval requests emitted on this pass. A retry
	// Watch yields an escalated-retry approval whose Extra list is empty.
	Approvals []ApprovalAction `json:"approvals,omitempty"`
}

// Options configures one pass. DryRun records the actions it would take
// without calling the Merger, writing the ledger or closing any Watch.
// Resolver supplies the dependency facts; a nil Resolver leaves every Watch
// with waitFor/waitForMain references waiting instead of merging blindly.
type Options struct {
	DryRun   bool
	Resolver DependencyResolver
	// CheckedMerger, when set, owns the mergeability gate and the post-merge
	// state confirmation. It replaces the plain Merger on the merge path. A nil
	// value keeps the trusted-on-success behaviour of Merger.
	CheckedMerger CheckedMerger
	// ReasonWaker, when set, receives the hold reason together with the wake. A
	// nil value sends only the verdict through Waker and leaves the reason on
	// the Action.
	ReasonWaker ReasonWaker
}

// Run walks the active Watches once. Ready Watches with autor-merge enabled
// are merged at most once per (watchID, revision), then closed. Watches held
// by dependencies produce a wait with a reason and wake their subscriber.
// Blocked Watches ask for one FIX agent per pass and escalate once the cap is
// reached. Retry/fail Watches are reported and handed to the Waker, never
// repaired here. Errors from individual Watches are collected so one bad Watch
// cannot hide the rest; the returned Result still describes everything.
func Run(ctx context.Context, st Store, merger Merger, waker Waker, opts Options) (Result, error) {
	return RunWithResolver(ctx, st, merger, waker, opts.Resolver, opts)
}

// RunWithResolver is Run with an explicit dependency resolver. It is the seam
// for callers that build the resolver outside Options. Passing a nil resolver
// only allows Watches without dependency references to merge.
func RunWithResolver(ctx context.Context, st Store, merger Merger, waker Waker, resolver DependencyResolver, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := Result{
		DryRun:      opts.DryRun,
		Merged:      []int{},
		Waiting:     []int{},
		NeedsRebase: []int{},
		Actions:     []Action{},
		Approvals:   []ApprovalAction{},
	}
	if st == nil {
		return out, errors.New("orchestrator: store is nil")
	}
	watches, err := st.ActiveWatches(ctx)
	if err != nil {
		observe.ActorError("orchestrator", "", err)
		return out, fmt.Errorf("orchestrator: active watches: %w", err)
	}
	started := time.Now()
	observe.RunStart(len(watches))
	var errs []error
	defer func() {
		stats := observe.CycleStats{
			Watches: len(watches), Merges: len(out.Merged), Wakes: len(out.Actions),
			Duration: time.Since(started),
		}
		observe.RunEnd(stats, errors.Join(errs...))
	}()

	for _, w := range watches {
		switch {
		case w.Blocked != nil:
			// Blocked wins over a stale ready verdict and over retry. Decide
			// reads the block first, so a blocked Watch can never merge here.
			handleBlocked(ctx, st, w, resolver, opts, &out, &errs)
		case w.isReady() || isWaitDependency(w.Verdict):
			// A Watch held on a previous tick is checked again with the same
			// gate. As soon as the ref is in main, this pass merges it.
			verdict, reason, err := w.canAutoMerge(ctx, resolver)
			if err != nil {
				observe.ActorError("orchestrator", w.ID, err)
				errs = append(errs, err)
				continue
			}
			switch verdict {
			case VerdictReady:
				observe.DependenciesResolved(w.ID, w.PR, nil)
				mergeOne(ctx, st, merger, opts, w, &out, &errs)
			case VerdictWait:
				// An unsatisfied dependency is nobody's fault: there is no fix
				// for an executor or a FIX agent to do, so no obligation is
				// staged and no subscriber is woken. The hold and its reason
				// are persisted instead and rechecked on the next tick.
				observe.DependencyWait(w.ID, w.PR, reason)
				out.Waiting = append(out.Waiting, w.PR)
				out.Actions = append(out.Actions, Action{
					Action: ActionWait, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
					Message: reason,
				})
				if holder, ok := st.(WatchHolder); ok && !opts.DryRun {
					if err := holder.HoldWatch(ctx, w.ID, WaitDependencyVerdict(reason)); err != nil {
						observe.ActorError("dependency-hold", w.ID, err)
						errs = append(errs, fmt.Errorf("orchestrator: hold watch %s: %w", w.ID, err))
					}
				}
			default:
				out.Actions = append(out.Actions, Action{
					Action: ActionSkip, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
					Message: reason,
				})
			}
		case needsWake(w.Verdict):
			out.NeedsRebase = append(out.NeedsRebase, w.PR)
			out.Actions = append(out.Actions, Action{
				Action:   ActionNeedsRebase,
				WatchID:  w.ID,
				PR:       w.PR,
				Revision: w.Revision,
				Message:  "left for subscriber wake: " + w.Verdict,
			})
			// A retry is the attempt that actually runs after a denial. Its
			// approval must describe the escalated retry, not the narrow first
			// attempt (codex-rs/core/src/tools/orchestrator.rs:521-532).
			if strings.EqualFold(strings.TrimSpace(w.Verdict), VerdictRetry) {
				_, retryApproval := ApprovalActions(w, nil)
				out.Approvals = append(out.Approvals, retryApproval)
			}
			wakeOne(ctx, waker, opts.ReasonWaker, w, w.Verdict, w.Verdict, &out, &errs)
		}
	}
	return out, errors.Join(errs...)
}

// handleBlocked turns one blocked Watch into exactly one decision per pass.
// Decide owns the FIX-attempt cap: below the cap the Watch asks for another FIX
// agent; at the cap the Watch is escalated to a human. There is no direct fixer
// spawn in this process, so one BLOCKED_FIX_AGENT decision is one reported
// attempt. The attempt is persisted on the Watch's block record so Decide sees
// the budget close: without the write BlockedInfo.FixAttempts stays zero and
// the Watch asks for a FIX agent forever. The switch in RunWithResolver calls
// this once per Watch, which keeps a single decision from charging twice.
func handleBlocked(ctx context.Context, st Store, w Watch, resolver DependencyResolver, opts Options, out *Result, errs *[]error) {
	d := Decide(w, resolver, CIState{})
	if d.Kind == DecisionNotifyOrchestrator && d.Reason == ReasonEscalateHuman {
		observe.BlockEscalated(w.ID, ReasonEscalateHuman)
		out.Actions = append(out.Actions, Action{
			Action: ActionBlockEscalated, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: ReasonEscalateHuman,
		})
		return
	}
	if d.Kind != DecisionBlockedFixAgent {
		return
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionBlockedFixAgent, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Message: d.Reason,
	})
	if opts.DryRun {
		return
	}
	// The block record is the sole budget Decide reads. Charge the attempt
	// here, where the FIX agent decision is emitted, so the third spawn closes
	// the cycle and the next pass escalates to a human. A failed write is
	// reported, not hidden: a lost charge would keep the loop spinning.
	if err := st.RecordFixAttempt(ctx, w.ID); err != nil {
		observe.ActorError("fix-attempt", w.ID, err)
		*errs = append(*errs, fmt.Errorf("orchestrator: record fix attempt %s: %w", w.ID, err))
	}
	observe.FixerSpawned(w.ID)
}

// mergeRetryMaxAttempts counts the first call and every retry. Three attempts
// keep one transient TLS failure from leaving a ready Watch untouched while
// bounding the time a single pass can spend on one PR.
const (
	mergeRetryMaxAttempts = 3
	mergeRetryBaseDelay   = 200 * time.Millisecond
	mergeRetryMaxDelay    = 2 * time.Second
)

// mergeRetrySleep is a seam for callers and tests that need a bounded pass.
// Production waits on the context so a shutdown interrupts a backoff.
var mergeRetrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mergeRetryDelay returns an exponential delay with full jitter. Jitter stops
// several Watches whose merges failed in the same tick from retrying in
// lockstep and producing another burst of TLS timeouts.
func mergeRetryDelay(attempt int) time.Duration {
	d := mergeRetryBaseDelay << (attempt - 1)
	if d > mergeRetryMaxDelay {
		d = mergeRetryMaxDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// retryTransient calls call until it succeeds, returns a non-transient error,
// or exhausts the bounded budget. Only errors that ghci classifies as
// transport-level are retried. A merge refusal or a conflict is never retried.
func retryTransient[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; attempt <= mergeRetryMaxAttempts; attempt++ {
		value, err := call()
		if err == nil || !ghci.IsTransientError(err) {
			return value, err
		}
		if attempt == mergeRetryMaxAttempts {
			return value, err
		}
		if sleepErr := mergeRetrySleep(ctx, mergeRetryDelay(attempt)); sleepErr != nil {
			return value, sleepErr
		}
	}
	return zero, nil
}

// recordMergeFailure is the single exit for a merge that failed after its
// budget. A transient failure earns an explicit WARN event. Without it a
// Watch still marked ready looks healthy although no retry is left.
func recordMergeFailure(w Watch, command string, err error, out *Result, errs *[]error) {
	observe.MergeFailure(w.ID, w.PR, err)
	observe.ActorError("merger", w.ID, err)
	if ghci.IsTransientError(err) {
		observe.Event(observe.LevelWarn, "merge_retry_exhausted", observe.Fields{
			observe.FieldWatchID: w.ID,
			observe.FieldPR:      w.PR,
			observe.FieldError:   err.Error(),
			"attempts":           mergeRetryMaxAttempts,
		})
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Command: command, Message: err.Error(),
	})
	*errs = append(*errs, fmt.Errorf("orchestrator: merge PR %d: %w", w.PR, err))
}

// mergeOne performs the idempotent merge branch for one ready Watch. A
// CheckedMerger passed in Options owns the gh mergeability gate and the
// post-merge state confirmation; without one the Merger is trusted on its
// success return. The caller chooses the guarantee explicitly.
func mergeOne(ctx context.Context, st Store, merger Merger, opts Options, w Watch, out *Result, errs *[]error) {
	started := time.Now()
	defer func() { observe.MergeDuration(time.Since(started)) }()
	done, err := st.WasMerged(ctx, w.ID, w.Revision)
	if err != nil {
		observe.ActorError("merge-ledger", w.ID, err)
		*errs = append(*errs, fmt.Errorf("orchestrator: merge ledger %s@%d: %w", w.ID, w.Revision, err))
		return
	}
	if done {
		observe.Event(observe.LevelInfo, "merge_skipped", observe.Fields{
			observe.FieldWatchID: w.ID, observe.FieldPR: w.PR,
			observe.FieldRevision: w.Revision, observe.FieldReason: "already merged at this revision",
		})
		out.Skipped = append(out.Skipped, w.PR)
		out.Actions = append(out.Actions, Action{
			Action: ActionSkip, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: "already merged at this revision",
		})
		return
	}

	command := fmt.Sprintf("gh pr merge %d --squash --delete-branch", w.PR)
	if opts.DryRun {
		out.Actions = append(out.Actions, Action{
			Action: ActionDryRun, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Command: command,
		})
		return
	}
	if merger == nil {
		mergerErr := fmt.Errorf("orchestrator: merger is nil for PR %d", w.PR)
		observe.MergeFailure(w.ID, w.PR, mergerErr)
		*errs = append(*errs, mergerErr)
		return
	}
	observe.MergeAttempt(w.ID, w.PR)
	if checked := opts.CheckedMerger; checked != nil {
		mergeCheckedOne(ctx, st, checked, w, command, out, errs)
		return
	}
	if _, err := retryTransient(ctx, func() (struct{}, error) {
		return struct{}{}, merger.Merge(ctx, w.Repo, w.PR)
	}); err != nil {
		recordMergeFailure(w, command, err, out, errs)
		return
	}
	confirmMerge(ctx, st, w, command, out, errs)
}

// mergeCheckedOne turns one checked outcome into state changes. Only a
// confirmed MERGE closes the Watch; a held or pending PR is reported without
// an error so a dirty PR does not produce a multi-line gh hint on every pass.
func mergeCheckedOne(ctx context.Context, st Store, merger CheckedMerger, w Watch, command string, out *Result, errs *[]error) {
	outcome, err := retryTransient(ctx, func() (MergeOutcome, error) {
		return merger.MergeChecked(ctx, w.Repo, w.PR)
	})
	if err != nil {
		log.Printf("orchestrator: merge PR %d: %v", w.PR, err)
		recordMergeFailure(w, command, err, out, errs)
		return
	}
	switch {
	case outcome.Merged:
		confirmMerge(ctx, st, w, command, out, errs)
	case outcome.Pending:
		observe.Event(observe.LevelInfo, "merge_pending", observe.Fields{
			observe.FieldWatchID: w.ID, observe.FieldPR: w.PR, observe.FieldRevision: w.Revision,
		})
		log.Printf("orchestrator: PR %d: merge command reported success, waiting for MERGE state", w.PR)
		out.Actions = append(out.Actions, Action{
			Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Command: command, Message: "waiting for MERGE state",
		})
	default:
		reason := outcome.Reason
		if reason == "" {
			reason = "merge gate held the PR"
		}
		observe.Event(observe.LevelInfo, "merge_held", observe.Fields{
			observe.FieldWatchID: w.ID, observe.FieldPR: w.PR,
			observe.FieldRevision: w.Revision, observe.FieldReason: reason,
		})
		log.Printf("orchestrator: PR %d: merge held: %s", w.PR, reason)
		out.Skipped = append(out.Skipped, w.PR)
		out.Actions = append(out.Actions, Action{
			Action: ActionSkip, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Message: reason,
		})
	}
}

// confirmMerge writes the ledger and closes the Watch for a PR the hosting
// service reported as MERGED. It is shared by checked and plain mergers.
func confirmMerge(ctx context.Context, st Store, w Watch, command string, out *Result, errs *[]error) {
	observe.MergeSuccess(w.ID, w.PR)
	if err := st.RecordMerge(ctx, w.ID, w.Revision); err != nil {
		observe.ActorError("merge-ledger", w.ID, err)
		*errs = append(*errs, fmt.Errorf("orchestrator: record merge %s@%d: %w", w.ID, w.Revision, err))
	}
	var closeErr error
	if closer, ok := st.(RevisionedWatchCloser); ok {
		closeErr = closer.CloseWatchAt(ctx, w.ID, w.Revision, w.HeadSHA)
	} else {
		closeErr = st.CloseWatch(ctx, w.ID)
	}
	if closeErr != nil {
		observe.ActorError("watch-closer", w.ID, closeErr)
		*errs = append(*errs, fmt.Errorf("orchestrator: close watch %s: %w", w.ID, closeErr))
	}
	out.Merged = append(out.Merged, w.PR)
	out.Actions = append(out.Actions, Action{
		Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Command: command,
	})
}

// wakeOne hands a retry/fail/wait Watch to the installed Waker. A nil Waker is
// legitimate in this card: the Watch simply stays active for later delivery.
// The reason is recorded on the Action and passed to an explicit ReasonWaker
// when the caller supplied one.
func wakeOne(ctx context.Context, waker Waker, reasonWaker ReasonWaker, w Watch, verdict, reason string, out *Result, errs *[]error) {
	if waker == nil {
		observe.WakeSkipped("", w.ID, "no waker installed")
		return
	}
	fields := observe.Fields{
		observe.FieldWatchID: w.ID, observe.FieldPR: w.PR,
		observe.FieldRevision: w.Revision, observe.FieldVerdict: verdict,
		observe.FieldReason: reason,
	}
	// Failed checks explain why the Watch came back as a retry. Writing the
	// hint only when checks exist keeps wakes without a block record unchanged.
	if w.Blocked != nil && len(w.Blocked.FailedChecks) != 0 {
		fields[observe.FieldDecisionHint] = strings.Join(w.Blocked.FailedChecks, ",")
	}
	observe.Event(observe.LevelInfo, "wake_requested", fields)
	// Only a rebase verdict asks for a rebase. A retry wake carries its own
	// verdict in wake_requested, so calling RebaseRequested here would report
	// a rebase that nobody asked for (T3.395).
	if verdict == VerdictRebase {
		observe.RebaseRequested(w.ID, w.PR)
	}
	var wakeErr error
	if reasonWaker != nil {
		wakeErr = reasonWaker.WakeWithReason(ctx, w.ID, w.Revision, verdict, reason)
	} else {
		wakeErr = waker.Wake(ctx, w.ID, w.Revision, verdict)
	}
	if wakeErr != nil {
		observe.WakeFailed("", w.ID, wakeErr)
		observe.ActorError("waker", w.ID, wakeErr)
		*errs = append(*errs, fmt.Errorf("orchestrator: wake %s: %w", w.ID, wakeErr))
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionWake, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Message: reason,
	})
}
