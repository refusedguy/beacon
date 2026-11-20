// Package orchestrator implements the automatic merge loop over active Beacon
// Watches. The core is pure over four narrow ports: Store reads active
// Watches, remembers completed merges and closes Watches; Merger performs the
// hosting-service merge; DependencyResolver resolves free-form waitFor and
// waitForMain references; Waker leaves a retry/fail/wait Watch for its
// subscriber. Concrete adapters live in this package and in cmd/beacon.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	// VerdictReady is the only verdict that permits a merge.
	VerdictReady = "ready"
	// VerdictWait is the hold decision when a dependency is not satisfied.
	// It carries a reason in the Action recorded for the Watch.
	VerdictWait = "wait"
	// VerdictRetry and VerdictFail are left for the subscriber's wake. This
	// card never repairs them; wake delivery belongs to another card.
	VerdictRetry = "retry"
	VerdictFail  = "fail"
)

// Action kinds recorded in Result.Actions.
const (
	ActionMerge       = "merge"
	ActionSkip        = "skip"
	ActionDryRun      = "dry-run"
	ActionNeedsRebase = "needs-rebase"
	ActionWake        = "wake"
	ActionWait        = "wait"
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

// Store is the surface the loop needs from Beacon state: active Watches, the
// durable (watchID, revision) merge ledger and Watch cancellation.
type Store interface {
	ActiveWatches(ctx context.Context) ([]Watch, error)
	WasMerged(ctx context.Context, watchID string, revision uint64) (bool, error)
	RecordMerge(ctx context.Context, watchID string, revision uint64) error
	CloseWatch(ctx context.Context, watchID string) error
}

// Waker asks a subscriber to act on a retry/fail/wait Watch. Wake delivery
// lands in another card; a nil Waker leaves those Watches untouched apart from
// being reported in Result.NeedsRebase and Result.Waiting.
type Waker interface {
	Wake(ctx context.Context, watchID string, revision uint64, verdict string) error
}

// ReasonWaker is an optional extension for decisions that need to explain
// themselves. A wait holds on a named dependency, so the subscriber needs the
// reason, not just the verdict. A Waker that does not implement it still gets
// the plain verdict and the reason stays on the Action.
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
}

// Options configures one pass. DryRun records the actions it would take
// without calling the Merger, writing the ledger or closing any Watch.
// Resolver supplies the dependency facts; a nil Resolver leaves every Watch
// with waitFor/waitForMain references waiting instead of merging blindly.
type Options struct {
	DryRun   bool
	Resolver DependencyResolver
}

// Run walks the active Watches once. Ready Watches with autor-merge enabled
// are merged at most once per (watchID, revision), then closed. Watches held
// by dependencies produce a wait with a reason and wake their subscriber.
// Retry/fail Watches are reported and handed to the Waker, never repaired
// here. Errors from individual Watches are collected so one bad Watch cannot
// hide the rest; the returned Result still describes everything that happened.
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
	}
	if st == nil {
		return out, errors.New("orchestrator: store is nil")
	}
	watches, err := st.ActiveWatches(ctx)
	if err != nil {
		return out, fmt.Errorf("orchestrator: active watches: %w", err)
	}

	var errs []error
	for _, w := range watches {
		switch {
		case w.isReady():
			verdict, reason, err := w.canAutoMerge(ctx, resolver)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			switch verdict {
			case VerdictReady:
				mergeOne(ctx, st, merger, opts, w, &out, &errs)
			case VerdictWait:
				out.Waiting = append(out.Waiting, w.PR)
				out.Actions = append(out.Actions, Action{
					Action: ActionWait, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
					Message: reason,
				})
				wakeOne(ctx, waker, w, VerdictWait, reason, &out, &errs)
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
			wakeOne(ctx, waker, w, w.Verdict, w.Verdict, &out, &errs)
		}
	}
	return out, errors.Join(errs...)
}

// mergeOne performs the idempotent merge branch for one ready Watch.
func mergeOne(ctx context.Context, st Store, merger Merger, opts Options, w Watch, out *Result, errs *[]error) {
	done, err := st.WasMerged(ctx, w.ID, w.Revision)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("orchestrator: merge ledger %s@%d: %w", w.ID, w.Revision, err))
		return
	}
	if done {
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
		*errs = append(*errs, fmt.Errorf("orchestrator: merger is nil for PR %d", w.PR))
		return
	}
	if err := merger.Merge(ctx, w.Repo, w.PR); err != nil {
		out.Actions = append(out.Actions, Action{
			Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
			Command: command, Message: err.Error(),
		})
		*errs = append(*errs, fmt.Errorf("orchestrator: merge PR %d: %w", w.PR, err))
		return
	}
	if err := st.RecordMerge(ctx, w.ID, w.Revision); err != nil {
		*errs = append(*errs, fmt.Errorf("orchestrator: record merge %s@%d: %w", w.ID, w.Revision, err))
	}
	if err := st.CloseWatch(ctx, w.ID); err != nil {
		*errs = append(*errs, fmt.Errorf("orchestrator: close watch %s: %w", w.ID, err))
	}
	out.Merged = append(out.Merged, w.PR)
	out.Actions = append(out.Actions, Action{
		Action: ActionMerge, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Command: command,
	})
}

// wakeOne hands a retry/fail/wait Watch to the installed Waker. A nil Waker is
// legitimate in this card: the Watch simply stays active for later delivery.
// The reason is recorded on the Action and passed to a ReasonWaker when the
// subscriber supports it.
func wakeOne(ctx context.Context, waker Waker, w Watch, verdict, reason string, out *Result, errs *[]error) {
	if waker == nil {
		return
	}
	if reasonWaker, ok := waker.(ReasonWaker); ok {
		if err := reasonWaker.WakeWithReason(ctx, w.ID, w.Revision, verdict, reason); err != nil {
			*errs = append(*errs, fmt.Errorf("orchestrator: wake %s: %w", w.ID, err))
		}
	} else if err := waker.Wake(ctx, w.ID, w.Revision, verdict); err != nil {
		*errs = append(*errs, fmt.Errorf("orchestrator: wake %s: %w", w.ID, err))
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionWake, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Message: reason,
	})
}
