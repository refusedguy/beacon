package core

import (
	"strings"
	"testing"
	"time"
)

type fakeCriterion struct {
	name    string
	verdict Verdict
	reason  string
}

func (f fakeCriterion) Name() string { return f.name }
func (f fakeCriterion) Evaluate(Observation) (Verdict, string) {
	return f.verdict, f.reason
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		obs  Observation
		rule Criterion
		want Verdict
	}{
		{"merge when rule passes", Observation{PRState: "open", Checks: []CheckResult{{Name: "ci", State: CheckSuccess}}}, fakeCriterion{name: "ok", verdict: VerdictMerge}, VerdictMerge},
		{"incomplete waits", Observation{PRState: "open", Incomplete: true}, fakeCriterion{name: "ok", verdict: VerdictMerge}, VerdictWait},
		{"wait when rule waits", Observation{PRState: "open"}, fakeCriterion{name: "checks", verdict: VerdictWait}, VerdictWait},
		{"conflict wins", Observation{PRState: "open", Conflict: true}, fakeCriterion{verdict: VerdictMerge}, VerdictNeedsAgentFix},
		{"failed check wins", Observation{PRState: "open", Checks: []CheckResult{{Name: "ci", State: CheckFailure}}}, fakeCriterion{verdict: VerdictMerge}, VerdictNeedsAgentFix},
		{"behind rebase", Observation{PRState: "open", Behind: true}, fakeCriterion{verdict: VerdictMerge}, VerdictNeedsRebase},
		{"closed is noop", Observation{PRState: "merged"}, nil, VerdictNoop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rules []Criterion
			if tc.rule != nil {
				rules = append(rules, tc.rule)
			}
			if got := Decide(tc.obs, rules).Verdict; got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDecideIncompleteNeverMerges(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  Observation
	}{
		{"empty checks", Observation{PRState: "open", Incomplete: true}},
		{"successful checks", Observation{PRState: "open", Incomplete: true, Checks: []CheckResult{{Name: "ci", State: CheckSuccess}}}},
		{"with conflict", Observation{PRState: "open", Incomplete: true, Conflict: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.obs, []Criterion{fakeCriterion{name: "ok", verdict: VerdictMerge}})
			if got.Verdict != VerdictWait {
				t.Fatalf("verdict = %s, want %s", got.Verdict, VerdictWait)
			}
		})
	}
	obs := Observation{PRState: "open", Incomplete: true, IncompleteReason: "jobs: provider down"}
	got := Decide(obs, nil)
	if got.Criterion != "invariant" || !strings.Contains(got.Reason, "jobs: provider down") {
		t.Fatalf("decision = %+v, want invariant with the failure reason", got)
	}
}

func TestDecideNoopRuleContinuesChain(t *testing.T) {
	obs := Observation{PRState: "open"}
	rules := []Criterion{
		fakeCriterion{name: "first", verdict: VerdictNoop},
		fakeCriterion{name: "second", verdict: VerdictNoop},
	}
	got := Decide(obs, rules)
	if got.Verdict != VerdictMerge {
		t.Fatalf("verdict = %s, want %s", got.Verdict, VerdictMerge)
	}
}

func TestDecideStopsAtFirstNonNoop(t *testing.T) {
	obs := Observation{PRState: "open"}
	rules := []Criterion{
		fakeCriterion{name: "first", verdict: VerdictNoop},
		fakeCriterion{name: "blocking", verdict: VerdictBlocked, reason: "gate is down"},
		fakeCriterion{name: "never-reached", verdict: VerdictWait, reason: "should not run"},
	}
	got := Decide(obs, rules)
	if got.Verdict != VerdictBlocked {
		t.Fatalf("verdict = %s, want %s", got.Verdict, VerdictBlocked)
	}
	if got.Criterion != "blocking" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "blocking")
	}
	if got.Reason != "gate is down" {
		t.Fatalf("reason = %q, want %q", got.Reason, "gate is down")
	}
}

func TestDecideDefaultReasonHasCriterionName(t *testing.T) {
	obs := Observation{PRState: "open"}
	rules := []Criterion{fakeCriterion{name: "custom", verdict: VerdictWait}}
	got := Decide(obs, rules)
	if !strings.Contains(got.Reason, "custom") {
		t.Fatalf("reason = %q, want it to name the criterion", got.Reason)
	}
}

func TestDecideUnresolvedThreads(t *testing.T) {
	obs := Observation{PRState: "open", UnresolvedThreads: []string{"please address the TODO"}}
	got := Decide(obs, nil)
	if got.Verdict != VerdictNeedsAgentFix {
		t.Fatalf("verdict = %s, want %s", got.Verdict, VerdictNeedsAgentFix)
	}
	if !strings.Contains(got.Reason, "please address the TODO") {
		t.Fatalf("reason = %q, want it to contain the thread text", got.Reason)
	}
	if got.Criterion != "invariant" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "invariant")
	}
}

func TestDecideClosedAndMergedAreNoop(t *testing.T) {
	for _, state := range []string{"closed", "merged", "CLOSED", "Merged"} {
		got := Decide(Observation{PRState: state}, nil)
		if got.Verdict != VerdictNoop {
			t.Fatalf("state %q: verdict = %s, want %s", state, got.Verdict, VerdictNoop)
		}
	}
}

func TestAllRequiredPass(t *testing.T) {
	cases := []struct {
		name   string
		checks []CheckResult
		names  []string
		want   bool
	}{
		{"no checks, no names", nil, nil, false},
		{"no checks, with names", nil, []string{"ci"}, false},
		{"all success, no names", []CheckResult{{Name: "a", State: CheckSuccess}, {Name: "b", State: CheckSuccess}}, nil, true},
		{"success and skipped, no names", []CheckResult{{Name: "a", State: CheckSuccess}, {Name: "b", State: CheckSkipped}}, nil, true},
		{"pending, no names", []CheckResult{{Name: "a", State: CheckSuccess}, {Name: "b", State: CheckPending}}, nil, false},
		{"failure, no names", []CheckResult{{Name: "a", State: CheckFailure}}, nil, false},
		{"unknown state, no names", []CheckResult{{Name: "a", State: CheckState("unknown")}}, nil, false},
		{"named check present and green", []CheckResult{{Name: "ci", State: CheckSuccess}}, []string{"ci"}, true},
		{"named check missing", []CheckResult{{Name: "other", State: CheckSuccess}}, []string{"ci"}, false},
		{"named check pending", []CheckResult{{Name: "ci", State: CheckPending}}, []string{"ci"}, false},
		{"named check case insensitive", []CheckResult{{Name: "CI", State: CheckSuccess}}, []string{"ci"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := Observation{Checks: tc.checks}
			if got := obs.AllRequiredPass(tc.names); got != tc.want {
				t.Fatalf("AllRequiredPass(%v) = %t, want %t", tc.names, got, tc.want)
			}
		})
	}
}

func TestCIWallSeconds(t *testing.T) {
	base := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }
	check := func(name string, start, end int) CheckResult {
		return CheckResult{Name: name, StartedAt: at(start), CompletedAt: at(end)}
	}
	cases := []struct {
		name   string
		checks []CheckResult
		want   int64
	}{
		{"empty", nil, 0},
		{"no timestamps", []CheckResult{{Name: "ci", State: CheckPending}}, 0},
		{"parallel", []CheckResult{check("lint", 0, 25), check("build", 0, 40)}, 40},
		{"sequential", []CheckResult{check("build", 0, 30), check("test", 30, 55)}, 55},
		{"staggered starts", []CheckResult{check("lint", 0, 20), check("test", 15, 60)}, 60},
		{"missing endpoint", []CheckResult{check("build", 0, 40), {Name: "lint", StartedAt: at(0)}}, 40},
		{"reversed endpoints", []CheckResult{check("build", 0, 40), check("bad", 30, 10)}, 40},
		{"single check", []CheckResult{check("build", 5, 35)}, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CIWallSeconds(tc.checks); got != tc.want {
				t.Fatalf("CIWallSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}
