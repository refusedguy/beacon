package core

import (
	"strings"
	"time"
)

type CheckState string

const (
	CheckSuccess CheckState = "success"
	CheckFailure CheckState = "failure"
	CheckPending CheckState = "pending"
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
}

func (o Observation) Failure() (CheckResult, bool) {
	for _, c := range o.Checks {
		if c.State == CheckFailure {
			return c, true
		}
	}
	return CheckResult{}, false
}

func (o Observation) AllRequiredPass(names []string) bool {
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

type Verdict string

const (
	VerdictWait          Verdict = "wait"
	VerdictMerge         Verdict = "merge"
	VerdictNeedsRebase   Verdict = "needs_rebase"
	VerdictNeedsAgentFix Verdict = "needs_agent_fix"
	VerdictBlocked       Verdict = "blocked"
	VerdictNoop          Verdict = "noop"
)

// Criterion returns VerdictNoop when it passed. Any other verdict stops the chain.
type Criterion interface {
	Name() string
	Evaluate(Observation) (Verdict, string)
}

type MergeDecision struct {
	Verdict   Verdict
	Reason    string
	Criterion string
}

// Decide applies invariants, then the configured criteria in order.
// It returns VerdictMerge only when every invariant and every rule passed.
func Decide(o Observation, criteria []Criterion) MergeDecision {
	switch strings.ToLower(o.PRState) {
	case "closed", "merged":
		return MergeDecision{Verdict: VerdictNoop, Reason: "pull request is " + o.PRState}
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
	if c, ok := o.Failure(); ok && !o.Behind {
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

type BaseStatus struct {
	BaseBranch     string
	HeadSHA        string
	FailedChecks   []CheckResult
	FailureExcerpt string
}

func (b BaseStatus) Red() bool {
	for _, c := range b.FailedChecks {
		if c.State == CheckFailure {
			return true
		}
	}
	return false
}

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
