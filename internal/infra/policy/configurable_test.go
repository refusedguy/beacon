package policy

import (
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestEvaluateAllGreenReturnsMerge(t *testing.T) {
	cfg := Config{
		RequiredChecks:    []string{"ci", "lint"},
		RequiredApprovals: 2,
		Labels:            []string{"ready", "automerge"},
		CommandGate:       "exit 0",
	}
	obs := core.Observation{
		PRState:   "open",
		Checks:    []core.CheckResult{{Name: "ci", State: core.CheckSuccess}, {Name: "lint", State: core.CheckSkipped}},
		Approvals: 2,
		Labels:    []string{"ready", "automerge"},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, core.VerdictMerge, got.Reason)
	}
}

func TestEvaluateEmptyRequiredChecksGreenReturnsMerge(t *testing.T) {
	cfg := Config{}
	obs := core.Observation{
		PRState: "open",
		Checks:  []core.CheckResult{{Name: "ci", State: core.CheckSuccess}, {Name: "docs", State: core.CheckSkipped}},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, core.VerdictMerge, got.Reason)
	}
}

func TestEvaluatePendingCheckReturnsWait(t *testing.T) {
	cfg := Config{RequiredChecks: []string{"ci"}}
	obs := core.Observation{
		PRState: "open",
		Checks:  []core.CheckResult{{Name: "ci", State: core.CheckPending}},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictWait {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictWait)
	}
	if got.Criterion != "required_checks" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "required_checks")
	}
	if !strings.Contains(got.Reason, "ci") {
		t.Fatalf("reason = %q, want it to name the check", got.Reason)
	}
}

func TestEvaluateFailedCheckReturnsNeedsAgentFix(t *testing.T) {
	cfg := Config{RequiredChecks: []string{"ci"}}
	obs := core.Observation{
		PRState: "open",
		Checks:  []core.CheckResult{{Name: "ci", State: core.CheckFailure}},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictNeedsAgentFix {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictNeedsAgentFix)
	}
	if got.Criterion != "invariant" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "invariant")
	}
}

func TestEvaluateBehindReturnsNeedsRebase(t *testing.T) {
	cfg := Config{RequiredChecks: []string{"ci"}}
	obs := core.Observation{
		PRState: "open",
		Behind:  true,
		Checks:  []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictNeedsRebase {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictNeedsRebase)
	}
}

func TestEvaluateConflictReturnsNeedsAgentFix(t *testing.T) {
	cfg := Config{RequiredChecks: []string{"ci"}}
	obs := core.Observation{
		PRState:  "open",
		Conflict: true,
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictNeedsAgentFix {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictNeedsAgentFix)
	}
}

func TestEvaluateMissingApprovalsReturnsWaitWithReason(t *testing.T) {
	cfg := Config{RequiredApprovals: 2}
	obs := core.Observation{PRState: "open", Approvals: 0}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictWait {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictWait)
	}
	if got.Criterion != "approvals" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "approvals")
	}
	for _, want := range []string{"2", "0"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason = %q, want it to contain %q", got.Reason, want)
		}
	}
}

func TestEvaluateMissingLabelReturnsWaitWithReason(t *testing.T) {
	cfg := Config{Labels: []string{"ready"}}
	obs := core.Observation{PRState: "open"}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictWait {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictWait)
	}
	if got.Criterion != "labels" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "labels")
	}
	if !strings.Contains(got.Reason, "ready") {
		t.Fatalf("reason = %q, want it to name the label", got.Reason)
	}
}

func TestEvaluateFailingCommandGateReturnsBlocked(t *testing.T) {
	cfg := Config{CommandGate: "exit 1"}
	obs := core.Observation{PRState: "open"}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictBlocked {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictBlocked)
	}
	if got.Criterion != "command_gate" {
		t.Fatalf("criterion = %q, want %q", got.Criterion, "command_gate")
	}
	if strings.TrimSpace(got.Reason) == "" {
		t.Fatal("reason must be non-empty for a failing command gate")
	}
}

func TestReviewRuleWaitsForApprovalUnlessSkipped(t *testing.T) {
	for _, tc := range []struct {
		mode      core.ReviewMode
		approvals int
		want      core.Verdict
	}{
		{core.ReviewModeLLM, 0, core.VerdictWait},
		{core.ReviewModeHuman, 0, core.VerdictWait},
		{core.ReviewModeLLM, 1, core.VerdictNoop},
		{core.ReviewModeHuman, 1, core.VerdictNoop},
		{core.ReviewModeNone, 0, core.VerdictNoop},
	} {
		rule := ReviewRule{Mode: tc.mode}
		got, _ := rule.Evaluate(core.Observation{PRState: "open", Approvals: tc.approvals})
		if got != tc.want {
			t.Errorf("mode=%s approvals=%d verdict=%s, want %s", tc.mode, tc.approvals, got, tc.want)
		}
	}
}

func TestConfigReviewModeAddsReviewCriterion(t *testing.T) {
	for _, mode := range []core.ReviewMode{core.ReviewModeLLM, core.ReviewModeHuman} {
		got := New(Config{ReviewMode: mode}).Evaluate(core.Observation{PRState: "open"})
		if got.Verdict != core.VerdictWait || got.Criterion != "review" {
			t.Errorf("mode=%s decision=%+v, want wait/review", mode, got)
		}
	}
	got := New(Config{ReviewMode: core.ReviewModeNone}).Evaluate(core.Observation{PRState: "open"})
	if got.Verdict != core.VerdictMerge {
		t.Fatalf("none mode decision = %+v, want merge", got)
	}
}

func TestRulePassReturnsNoopWithEmptyReason(t *testing.T) {
	obs := core.Observation{
		PRState:   "open",
		Checks:    []core.CheckResult{{Name: "ci", State: core.CheckSuccess}},
		Approvals: 1,
		Labels:    []string{"ready"},
	}
	rules := []core.Criterion{
		ChecksRule{Names: []string{"ci"}},
		ApprovalsRule{Needed: 1},
		LabelRule{Names: []string{"ready"}},
		CommandRule{Command: "exit 0"},
	}
	for _, rule := range rules {
		verdict, reason := rule.Evaluate(obs)
		if verdict != core.VerdictNoop {
			t.Errorf("%s: passing verdict = %s, want %s", rule.Name(), verdict, core.VerdictNoop)
		}
		if reason != "" {
			t.Errorf("%s: passing reason = %q, want empty", rule.Name(), reason)
		}
	}
}

func TestRuleFailReturnsConcreteReason(t *testing.T) {
	obs := core.Observation{PRState: "open"}
	rules := []core.Criterion{
		ChecksRule{Names: []string{"ci"}},
		ApprovalsRule{Needed: 1},
		LabelRule{Names: []string{"ready"}},
		CommandRule{Command: "exit 1"},
	}
	for _, rule := range rules {
		verdict, reason := rule.Evaluate(obs)
		want := core.VerdictWait
		if rule.Name() == "command_gate" {
			want = core.VerdictBlocked
		}
		if verdict != want {
			t.Errorf("%s: failing verdict = %s, want %s", rule.Name(), verdict, want)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s: failing reason must be non-empty", rule.Name())
		}
	}
}
