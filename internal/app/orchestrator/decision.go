package orchestrator

import "strings"

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
)

// BlockedInfo is why an executor blocked a Watch. It mirrors the domain block
// details without importing domain types into this package (ADR 0005).
type BlockedInfo struct {
	Reason       string   `json:"reason"`
	FailedChecks []string `json:"failedChecks,omitempty"`
	BlockedBy    string   `json:"blockedBy,omitempty"`
}

// DependencyResolver answers whether an opaque waitFor ref has landed in main.
// An error or an unknown ref means "not yet": the decision holds the merge
// instead of guessing that a dependency is satisfied.
type DependencyResolver interface {
	InMain(ref string) (bool, error)
}

// DependencyResolverFunc adapts a function to DependencyResolver.
type DependencyResolverFunc func(ref string) (bool, error)

// InMain calls f. A nil function reports the ref as unsatisfied.
func (f DependencyResolverFunc) InMain(ref string) (bool, error) {
	if f == nil {
		return false, nil
	}
	return f(ref)
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
}

// Decide reduces a Watch, its dependency resolver and the CI view to exactly
// one Decision. It has no side effects: the caller owns the merge ledger and
// the wake delivery. Priority is fixed and does not depend on field order:
//
//  1. a blocked Watch always goes to the FIX agent, even if its verdict still
//     reads ready or a dependency is outstanding;
//  2. unsatisfied waitFor/waitForMain refs hold the merge and list the refs;
//  3. ready plus auto-merge merges;
//  4. retry goes back to the executor;
//  5. anything else is escalated, never merged.
func Decide(w Watch, deps DependencyResolver, ci CIState) Decision {
	d := Decision{WatchID: w.ID, PR: w.PR, Revision: w.Revision}

	if w.Blocked != nil {
		d.Kind = DecisionBlockedFixAgent
		d.Reason = w.Blocked.Reason
		d.BlockedBy = w.Blocked.BlockedBy
		d.FailedChecks = append([]string(nil), w.Blocked.FailedChecks...)
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
			inMain := false
			if deps != nil {
				inMain, _ = deps.InMain(ref)
			}
			if !inMain {
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
