package core

import "testing"

// requiredCheckCriterion implements the narrow capability Decide uses to scope
// the failure invariant to required checks.
type requiredCheckCriterion struct {
	names []string
}

func (c requiredCheckCriterion) Name() string { return "required_checks" }
func (c requiredCheckCriterion) Evaluate(Observation) (Verdict, string) {
	return VerdictNoop, ""
}
func (c requiredCheckCriterion) RequiredChecks() ([]string, bool) {
	return c.names, true
}

func TestDecideIgnoresFailedOptionalCheck(t *testing.T) {
	obs := Observation{
		PRState: "open",
		Checks: []CheckResult{
			{Name: "ci", State: CheckSuccess},
			{Name: "manual", State: CheckFailure},
		},
	}
	got := Decide(obs, []Criterion{requiredCheckCriterion{names: []string{"ci"}}})
	if got.Verdict != VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, VerdictMerge, got.Reason)
	}
}

func TestDecideStillBlocksFailedRequiredCheck(t *testing.T) {
	obs := Observation{
		PRState: "open",
		Checks: []CheckResult{
			{Name: "lint", State: CheckFailure},
			{Name: "ci", State: CheckSuccess},
		},
	}
	got := Decide(obs, []Criterion{requiredCheckCriterion{names: []string{"lint"}}})
	if got.Verdict != VerdictNeedsAgentFix {
		t.Fatalf("verdict = %s, want %s", got.Verdict, VerdictNeedsAgentFix)
	}
}

func TestRunApplyClearsReasonOnRecovery(t *testing.T) {
	for _, kind := range []EventKind{EvAgentFixed, EvChecksPassed, EvRebased} {
		t.Run(string(kind), func(t *testing.T) {
			state := RunWaitingCI
			if kind == EvAgentFixed {
				state = RunNeedsAgentFix
			}
			if kind == EvRebased {
				state = RunNeedsRebase
			}
			run := &Run{ID: "r1", State: state, Reason: "required check failed: lint"}
			if err := run.Apply(Event{Kind: kind}); err != nil {
				t.Fatalf("run.Apply(%s): %v", kind, err)
			}
			if run.Reason != "" {
				t.Fatalf("reason = %q, want cleared", run.Reason)
			}
		})
	}
}
