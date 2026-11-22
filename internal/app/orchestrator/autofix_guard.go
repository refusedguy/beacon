package orchestrator

// AutofixFacts holds the signals that decide whether the orchestrator may
// spawn a fixer for a PR. The caller fills it from data the cycle already has.
// The guard never reads CI or the base branch itself.
type AutofixFacts struct {
	// HeadPusherIsAgent is true when the agent, not a human, pushed the head
	// commit. A human push means the person took over the branch.
	HeadPusherIsAgent bool
	// FollowUpSinceLastAttempt is true when someone sent a follow-up after the
	// last fixer attempt.
	FollowUpSinceLastAttempt bool
	// BaseFailedChecks holds checks that already fail on the base branch. It is
	// the set the verdict layer computed, passed through unchanged.
	BaseFailedChecks []string
	// AttemptCount counts the CI follow-ups the PR has already produced.
	AttemptCount int
	// OptedOut is true when the PR owner asked to disable autofix.
	OptedOut bool
}

// AutofixSkipReason explains why a fixer spawn is withheld. The zero value
// means the attempt may go ahead.
type AutofixSkipReason int

const (
	// AutofixAllowed lets the fixer spawn proceed.
	AutofixAllowed AutofixSkipReason = iota
	// AutofixSkipOptedOut records an owner request to disable autofix.
	AutofixSkipOptedOut
	// AutofixSkipHumanHead records a head commit pushed by a human.
	AutofixSkipHumanHead
	// AutofixSkipFollowUp records a follow-up after the last fixer attempt.
	AutofixSkipFollowUp
	// AutofixSkipBaseFailure records a failure already present on the base.
	AutofixSkipBaseFailure
	// AutofixSkipAttemptLimit records a PR that spent its fixer budget.
	AutofixSkipAttemptLimit
)

// String returns a stable name for logs and runbook output.
func (r AutofixSkipReason) String() string {
	switch r {
	case AutofixAllowed:
		return "allowed"
	case AutofixSkipOptedOut:
		return "owner opted out"
	case AutofixSkipHumanHead:
		return "human pushed head"
	case AutofixSkipFollowUp:
		return "follow-up since last attempt"
	case AutofixSkipBaseFailure:
		return "base branch failure"
	case AutofixSkipAttemptLimit:
		return "attempt limit reached"
	default:
		return "unknown"
	}
}

// DefaultAutofixAttemptLimit is the number of CI follow-ups a PR may spend on
// autofix before the guard holds further fixer spawns.
const DefaultAutofixAttemptLimit = 10

// AllowAutofix returns the first reason to withhold a fixer spawn, or
// AutofixAllowed when no skip rule matches. Rules run in priority order, so
// several matching facts still give one answer. A non-positive limit selects
// DefaultAutofixAttemptLimit.
//
// The guard only holds the fixer spawn. It does not change the wake sent to the
// orchestrator, and it does not read CI state. BaseFailedChecks comes from the
// verdict layer.
func AllowAutofix(facts AutofixFacts, attemptLimit int) AutofixSkipReason {
	switch {
	case facts.OptedOut:
		return AutofixSkipOptedOut
	case !facts.HeadPusherIsAgent:
		return AutofixSkipHumanHead
	case facts.FollowUpSinceLastAttempt:
		return AutofixSkipFollowUp
	case len(facts.BaseFailedChecks) > 0:
		return AutofixSkipBaseFailure
	}

	if attemptLimit <= 0 {
		attemptLimit = DefaultAutofixAttemptLimit
	}
	if facts.AttemptCount >= attemptLimit {
		return AutofixSkipAttemptLimit
	}
	return AutofixAllowed
}
