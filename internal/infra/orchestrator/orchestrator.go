// Package orchestrator implements the automatic merge loop over active Beacon
// Watches. The core is pure over three narrow ports: Store reads active
// Watches, remembers completed merges and closes Watches; Merger performs the
// hosting-service merge; Waker leaves a retry/fail Watch for its subscriber.
// Concrete adapters live in this package and in cmd/beacon.
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
)

// AutoMergePolicy is the autor-merge policy of one Watch. AutoMerge is a
// pointer so the absent value means "default true": a Watch with no declared
// policy is mergeable. Any of the three hold-backs disables automatic merging.
type AutoMergePolicy struct {
	AutoMerge       *bool  `json:"autoMerge,omitempty"`
	RequireApproval bool   `json:"requireApproval"`
	WaitFor         string `json:"waitFor"`
	WaitForMain     bool   `json:"waitForMain"`
}

// DefaultAutoMergePolicy is the zero-configuration policy: automatic merging
// is on and no hold-back is set.
func DefaultAutoMergePolicy() AutoMergePolicy {
	return AutoMergePolicy{}
}

// Allows reports whether this policy permits an automatic merge.
func (p AutoMergePolicy) Allows() bool {
	if p.AutoMerge != nil && !*p.AutoMerge {
		return false
	}
	return !p.RequireApproval &&
		strings.TrimSpace(p.WaitFor) == "" && !p.WaitForMain
}

// Watch is the subset of a Beacon Watch the loop decides on. A nil Policy
// means the default autor-merge policy applies.
type Watch struct {
	ID       string
	Repo     string
	PR       int
	Verdict  string
	Revision uint64
	Policy   *AutoMergePolicy
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

// Waker asks a subscriber to act on a retry/fail Watch. Wake delivery lands in
// another card; a nil Waker leaves those Watches untouched apart from being
// reported in Result.NeedsRebase.
type Waker interface {
	Wake(ctx context.Context, watchID string, revision uint64, verdict string) error
}

// Action is one decision the loop made, recorded for output and diagnostics.
type Action struct {
	Action   string `json:"action"`
	WatchID  string `json:"watchID"`
	PR       int    `json:"pr"`
	Revision uint64 `json:"revision"`
	Command  string `json:"command,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Result is the outcome of one orchestrate pass. NeedsRebase lists the PRs of
// retry/fail Watches that were left for their subscribers.
type Result struct {
	DryRun      bool     `json:"dryRun"`
	Merged      []int    `json:"merged"`
	Skipped     []int    `json:"skipped,omitempty"`
	NeedsRebase []int    `json:"needsRebase"`
	Actions     []Action `json:"actions"`
}

// Options configures one pass. DryRun records the actions it would take
// without calling the Merger, writing the ledger or closing any Watch.
type Options struct {
	DryRun bool
}

// Run walks the active Watches once. Ready Watches with autor-merge enabled
// are merged at most once per (watchID, revision), then closed. Retry/fail
// Watches are reported and handed to the Waker, never repaired here. Errors
// from individual Watches are collected so one bad Watch cannot hide the rest;
// the returned Result still describes everything that happened.
func Run(ctx context.Context, st Store, merger Merger, waker Waker, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := Result{
		DryRun:      opts.DryRun,
		Merged:      []int{},
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
		case w.isReady() && w.autoMergeAllowed():
			mergeOne(ctx, st, merger, opts, w, &out, &errs)
		case needsWake(w.Verdict):
			out.NeedsRebase = append(out.NeedsRebase, w.PR)
			out.Actions = append(out.Actions, Action{
				Action:   ActionNeedsRebase,
				WatchID:  w.ID,
				PR:       w.PR,
				Revision: w.Revision,
				Message:  "left for subscriber wake: " + w.Verdict,
			})
			wakeOne(ctx, waker, w, &out, &errs)
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

// wakeOne hands a retry/fail Watch to the installed Waker. A nil Waker is
// legitimate in this card: the Watch simply stays active for later delivery.
func wakeOne(ctx context.Context, waker Waker, w Watch, out *Result, errs *[]error) {
	if waker == nil {
		return
	}
	if err := waker.Wake(ctx, w.ID, w.Revision, w.Verdict); err != nil {
		*errs = append(*errs, fmt.Errorf("orchestrator: wake %s: %w", w.ID, err))
	}
	out.Actions = append(out.Actions, Action{
		Action: ActionWake, WatchID: w.ID, PR: w.PR, Revision: w.Revision,
		Message: w.Verdict,
	})
}
