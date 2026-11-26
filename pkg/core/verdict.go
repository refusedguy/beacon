package core

import (
	"strings"
	"time"
)

// CheckState is the conclusion of one CI check.
type CheckState string

const (
	// CheckSuccess means the check passed.
	CheckSuccess CheckState = "success"
	// CheckFailure means the check failed.
	CheckFailure CheckState = "failure"
	// CheckPending means the check has not finished.
	CheckPending CheckState = "pending"
	// CheckSkipped means the check was skipped.
	CheckSkipped CheckState = "skipped"
)

// CheckResult is one CI check. StartedAt and CompletedAt are zero when the
// provider does not report timing, so callers can derive a duration safely.
type CheckResult struct {
	Name        string
	State       CheckState
	Output      string
	StartedAt   time.Time
	CompletedAt time.Time
}

// DurationSeconds is the check's wall-clock time. It is zero when either
// endpoint is missing or the completed time precedes the start.
func (c CheckResult) DurationSeconds() int64 {
	if c.StartedAt.IsZero() || c.CompletedAt.IsZero() || c.CompletedAt.Before(c.StartedAt) {
		return 0
	}
	return int64(c.CompletedAt.Sub(c.StartedAt).Seconds())
}

// CIWallSeconds is the span from the earliest check start to the latest check
// completion. Checks missing either endpoint are ignored. It is zero when no
// check reports both endpoints.
func CIWallSeconds(checks []CheckResult) int64 {
	var start, end time.Time
	for _, c := range checks {
		if c.StartedAt.IsZero() || c.CompletedAt.IsZero() || c.CompletedAt.Before(c.StartedAt) {
			continue
		}
		if start.IsZero() || c.StartedAt.Before(start) {
			start = c.StartedAt
		}
		if end.IsZero() || c.CompletedAt.After(end) {
			end = c.CompletedAt
		}
	}
	if start.IsZero() {
		return 0
	}
	return int64(end.Sub(start).Seconds())
}

// PRState is the state a provider reports for a pull request. GitHub and
// GitLab use different tokens for an open request, so callers classify state
// through IsClosed and IsMerged instead of comparing raw strings.
type PRState string

const (
	// PROpen is GitHub's open pull request state.
	PROpen PRState = "open"
	// PROpened is GitLab's open merge request state.
	PROpened PRState = "opened"
	// PRClosed is a pull request closed without a merge.
	PRClosed PRState = "closed"
	// PRMerged is a pull request closed by a merge.
	PRMerged PRState = "merged"
	// PRLocked is a closed and locked pull request.
	PRLocked PRState = "locked"
)

// IsClosed reports whether s is a final state. Closed, merged and locked pull
// requests are final; an open or unknown state is not.
func (s PRState) IsClosed() bool {
	switch strings.ToLower(string(s)) {
	case string(PRClosed), string(PRMerged), string(PRLocked):
		return true
	default:
		return false
	}
}

// IsMerged reports whether s is a merged pull request.
func (s PRState) IsMerged() bool {
	return strings.ToLower(string(s)) == string(PRMerged)
}

// FinalState is the shared classification of a provider's pull request state.
type FinalState struct {
	// Closed reports whether the request is closed, merged or locked.
	Closed bool
	// Merged reports whether the request was merged.
	Merged bool
}

// DecideFinalState classifies a raw provider state once for every final-state
// handler. Open, unknown and empty states are not final, so no handler can
// finalize a pull request Beacon still treats as open.
func DecideFinalState(raw string) FinalState {
	state := PRState(raw)
	return FinalState{Closed: state.IsClosed(), Merged: state.IsMerged()}
}

// Observation is a provider snapshot of a pull request at one point in time.
type Observation struct {
	PRNumber          int
	HeadSHA           string
	BaseSHA           string
	PRState           string
	Checks            []CheckResult
	Approvals         int
	Labels            []string
	Behind            bool
	Conflict          bool
	FailureExcerpt    string
	UnresolvedThreads []string
	// WorkflowSkipped reports that the provider ran no checks because the CI
	// workflow skipped this diff. Adapters set it only for changes whose paths
	// the workflow ignores. It is never inferred from an empty check list.
	WorkflowSkipped bool
	// Incomplete reports that a provider call failed or its response could not
	// be trusted. Decide returns VerdictWait for it, so a partial observation
	// can never produce a merge verdict.
	Incomplete bool
	// IncompleteReason names the failed provider calls. It is empty when
	// Incomplete is false.
	IncompleteReason string
}

// Failure returns the first failed check.
func (o Observation) Failure() (CheckResult, bool) {
	for _, c := range o.Checks {
		if c.State == CheckFailure {
			return c, true
		}
	}
	return CheckResult{}, false
}

// HasPending reports whether any check is still running. A behind pull request
// with a pending check can still turn red, so it must wait for a verdict before
// the reconciler updates its branch.
func (o Observation) HasPending() bool {
	for _, c := range o.Checks {
		if c.State == CheckPending {
			return true
		}
	}
	return false
}

// AllRequiredPass reports whether every named check passed or was skipped.
func (o Observation) AllRequiredPass(names []string) bool {
	if len(o.Checks) == 0 {
		return false
	}
	if len(names) == 0 {
		for _, c := range o.Checks {
			if c.State != CheckSuccess && c.State != CheckSkipped {
				return false
			}
		}
		return true
	}
	for _, want := range names {
		found := false
		for _, c := range o.Checks {
			if strings.EqualFold(c.Name, want) {
				found = true
				if c.State != CheckSuccess && c.State != CheckSkipped {
					return false
				}
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Verdict is the reconciler's decision for one observation.
type Verdict string

const (
	// VerdictWait means the reconciler should observe again later.
	VerdictWait Verdict = "wait"
	// VerdictMerge means the pull request is ready to merge.
	VerdictMerge Verdict = "merge"
	// VerdictNeedsRebase means the branch must be rebased.
	VerdictNeedsRebase Verdict = "needs_rebase"
	// VerdictNeedsAgentFix means an agent must fix the pull request.
	VerdictNeedsAgentFix Verdict = "needs_agent_fix"
	// VerdictBlocked means an incident blocks the pull request.
	VerdictBlocked Verdict = "blocked"
	// VerdictNoop means no action is needed.
	VerdictNoop Verdict = "noop"
)

// ActionDispatch selects the reconcile handler a verdict runs.
type ActionDispatch string

const (
	DispatchNoop    ActionDispatch = "noop"
	DispatchMerge   ActionDispatch = "merge"
	DispatchRebase  ActionDispatch = "rebase"
	DispatchFix     ActionDispatch = "fix"
	DispatchBlock   ActionDispatch = "block"
	DispatchPending ActionDispatch = "pending"
)

// ActionCounter names the metric a decision category increments.
type ActionCounter string

const (
	CounterDispatchCycles ActionCounter = "dispatch_cycles"
	CounterSkip           ActionCounter = "skip"
	CounterDispatch       ActionCounter = "dispatch"
	CounterRebase         ActionCounter = "rebase"
	CounterFix            ActionCounter = "fix"
)

// DecisionAction is one row of the verdict and decision-kind category table.
// Label is the action written to decision records, Dispatch selects the
// reconcile handler, and Counter names the metric a decision increments.
// Qualifier gates a counter on a transition result such as "rebased".
type DecisionAction struct {
	Label     string
	Dispatch  ActionDispatch
	Counter   ActionCounter
	Qualifier string
}

// decisionActions is the single table for verdicts and decision kinds. Keys
// are verdict strings and decision-kind strings. One row carries every output
// the taxonomy drives, so a new verdict or kind needs one edit here.
var decisionActions = map[string]DecisionAction{
	string(VerdictMerge):         {Label: "merge", Dispatch: DispatchMerge},
	string(VerdictNeedsRebase):   {Label: "rebase", Dispatch: DispatchRebase},
	string(VerdictNeedsAgentFix): {Label: "wake", Dispatch: DispatchFix},
	string(VerdictBlocked):       {Label: "block", Dispatch: DispatchBlock},
	string(VerdictNoop):          {Label: "noop", Dispatch: DispatchNoop},
	string(VerdictWait):          {Label: "wait", Dispatch: DispatchPending},

	"dispatch.candidates": {Counter: CounterDispatchCycles},
	"dispatch.skip":       {Counter: CounterSkip},
	"transition.spawn":    {Counter: CounterDispatch},
	"transition.rebase":   {Counter: CounterRebase, Qualifier: "rebased"},
	"transition.wake":     {Counter: CounterFix, Qualifier: "woke"},
}

// ActionForVerdict returns the table row for v. A verdict outside the known
// taxonomy takes the wait row, which keeps the reconciler's default branch.
func ActionForVerdict(v Verdict) DecisionAction {
	if action, ok := decisionActions[string(v)]; ok {
		return action
	}
	return decisionActions[string(VerdictWait)]
}

// ActionForKind returns the table row for a decision kind. An unknown kind
// returns the zero row, which increments no counter.
func ActionForKind(kind string) DecisionAction {
	return decisionActions[kind]
}

// Criterion returns VerdictNoop when it passed. Any other verdict stops the chain.
type Criterion interface {
	Name() string
	Evaluate(Observation) (Verdict, string)
}

// MergeDecision is the outcome of one policy evaluation.
type MergeDecision struct {
	Verdict   Verdict
	Reason    string
	Criterion string
}

// Decide applies invariants, then the configured criteria in order.
// It returns VerdictMerge only when every invariant and every rule passed.
func Decide(o Observation, criteria []Criterion) MergeDecision {
	if DecideFinalState(o.PRState).Closed {
		return MergeDecision{Verdict: VerdictNoop, Reason: "pull request is " + o.PRState}
	}
	if o.Incomplete {
		reason := "observation is incomplete"
		if o.IncompleteReason != "" {
			reason += ": " + o.IncompleteReason
		}
		return MergeDecision{Verdict: VerdictWait, Reason: reason, Criterion: "invariant"}
	}
	if o.Conflict {
		return MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: "merge conflict", Criterion: "invariant"}
	}
	if len(o.UnresolvedThreads) > 0 {
		return MergeDecision{
			Verdict:   VerdictNeedsAgentFix,
			Reason:    "unresolved discussion: " + o.UnresolvedThreads[0],
			Criterion: "invariant",
		}
	}
	if c, ok := o.Failure(); ok {
		reason := "required check failed: " + c.Name
		if o.FailureExcerpt != "" {
			reason += ": " + o.FailureExcerpt
		}
		return MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: reason, Criterion: "invariant"}
	}
	if o.Behind {
		return MergeDecision{Verdict: VerdictNeedsRebase, Reason: "branch is behind base", Criterion: "invariant"}
	}
	for _, rule := range criteria {
		verdict, reason := rule.Evaluate(o)
		if verdict == VerdictNoop {
			continue
		}
		if reason == "" {
			reason = "criterion " + rule.Name() + " did not pass"
		}
		return MergeDecision{Verdict: verdict, Reason: reason, Criterion: rule.Name()}
	}
	return MergeDecision{Verdict: VerdictMerge, Reason: "all criteria passed"}
}

// BaseStatus describes the CI status of a base branch.
type BaseStatus struct {
	BaseBranch     string
	HeadSHA        string
	FailedChecks   []CheckResult
	FailureExcerpt string
}

// Red reports whether a failed check is present.
func (b BaseStatus) Red() bool {
	for _, c := range b.FailedChecks {
		if c.State == CheckFailure {
			return true
		}
	}
	return false
}

// BaseIncident records a red base branch and its fixer run.
type BaseIncident struct {
	ID               string
	Repo             string
	BaseBranch       string
	Signature        string
	FailedChecksJSON string
	FixerRunID       RunID
	Status           string
	Attempts         int
	CreatedAt        int64
	ResolvedAt       int64
}
