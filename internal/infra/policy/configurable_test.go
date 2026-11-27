package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// fakeCheckSource returns a fixed list of names, with an optional error. It
// stands in for any provider implementation of the port.
type fakeCheckSource struct {
	names []string
	err   error
}

func (f fakeCheckSource) Names(ctx context.Context) ([]string, error) {
	return f.names, f.err
}

func TestEvaluateAllGreenReturnsMerge(t *testing.T) {
	cfg := Config{
		RequiredCheckSource: fakeCheckSource{names: []string{"ci", "lint"}},
		RequiredApprovals:   2,
		Labels:              []string{"ready", "automerge"},
		CommandGate:         "exit 0",
	}
	obs := core.Observation{
		PRState:   "open",
		Checks:    []core.CheckResult{{Name: "ci", State: core.CheckSuccess}, {Name: "lint", State: core.CheckSuccess}},
		Approvals: 2,
		Labels:    []string{"ready", "automerge"},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, core.VerdictMerge, got.Reason)
	}
}

func TestEvaluateSkippedRequiredCheckWaitsWithoutDiffSkip(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build"}}}
	obs := core.Observation{
		PRState: "open",
		Checks:  []core.CheckResult{{Name: "build", State: core.CheckSkipped}},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictWait || got.Criterion != "required_checks" {
		t.Fatalf("decision = %+v, want wait/required_checks", got)
	}
	if !strings.Contains(got.Reason, "build (skipped)") {
		t.Fatalf("reason = %q, want the skipped check named", got.Reason)
	}
}

func TestEvaluateDeclaredSkipAcceptsSkippedRequiredCheck(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build"}}}
	obs := core.Observation{
		PRState:         "open",
		WorkflowSkipped: true,
		Checks:          []core.CheckResult{{Name: "build", State: core.CheckSkipped}},
	}
	if got := New(cfg).Evaluate(obs); got.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, core.VerdictMerge, got.Reason)
	}
}

func TestEvaluateCancelledAndSkippedRequiredChecks(t *testing.T) {
	cases := []struct {
		name            string
		state           core.CheckState
		workflowSkipped bool
		want            core.Verdict
	}{
		{"cancelled", core.CheckCancelled, false, core.VerdictWait},
		{"cancelled with declared workflow skip", core.CheckCancelled, true, core.VerdictWait},
		{"skipped with declared workflow skip", core.CheckSkipped, true, core.VerdictMerge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build"}}}
			obs := core.Observation{
				PRState:         "open",
				WorkflowSkipped: tc.workflowSkipped,
				Checks:          []core.CheckResult{{Name: "build", State: tc.state}},
			}
			got := New(cfg).Evaluate(obs)
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, tc.want, got.Reason)
			}
			if tc.want == core.VerdictWait {
				if got.Criterion != "required_checks" {
					t.Fatalf("criterion = %q, want %q", got.Criterion, "required_checks")
				}
				if !strings.Contains(got.Reason, "build ("+string(tc.state)+")") {
					t.Fatalf("reason = %q, want the %s check named", got.Reason, tc.state)
				}
			}
		})
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
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"ci"}}}
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

func TestEvaluateWorkflowSkippedDocsOnlyWithNoChecksMerges(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build", "vet", "test", "lint"}}}
	obs := core.Observation{PRState: "open", WorkflowSkipped: true}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, core.VerdictMerge, got.Reason)
	}
}

func TestEvaluateWorkflowSkippedWithFailureBlocks(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build"}}}
	obs := core.Observation{
		PRState:         "open",
		WorkflowSkipped: true,
		Checks:          []core.CheckResult{{Name: "build", State: core.CheckFailure}},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictNeedsAgentFix {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictNeedsAgentFix)
	}
	if got.Criterion != "invariant" {
		t.Fatalf("criterion = %q, want invariant", got.Criterion)
	}
	if verdict, _ := (SourceChecksRule{Source: cfg.RequiredCheckSource}).Evaluate(obs); verdict == core.VerdictNoop {
		t.Fatal("SourceChecksRule returned noop for a reported failure")
	}
}

func TestEvaluateIgnoresOptionalFailedCheck(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build"}}}
	obs := core.Observation{
		PRState: "open",
		Checks: []core.CheckResult{
			{Name: "build", State: core.CheckSuccess},
			{Name: "manual", State: core.CheckFailure},
		},
	}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictMerge {
		t.Fatalf("verdict = %s, want %s (reason %q)", got.Verdict, core.VerdictMerge, got.Reason)
	}
}

func TestEvaluateMissingChecksWithoutSkipWaitsWithCriterion(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build", "lint"}}}
	obs := core.Observation{PRState: "open"}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictWait {
		t.Fatalf("verdict = %s, want %s", got.Verdict, core.VerdictWait)
	}
	if got.Criterion != "required_checks" {
		t.Fatalf("criterion = %q, want required_checks", got.Criterion)
	}
	for _, want := range []string{"build", "lint", "missing"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason = %q, want it to contain %q", got.Reason, want)
		}
	}
}

func TestEvaluateWorkflowSkippedStillAppliesOtherCriteria(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"build"}}, Labels: []string{"ready"}}
	obs := core.Observation{PRState: "open", WorkflowSkipped: true}
	got := New(cfg).Evaluate(obs)
	if got.Verdict != core.VerdictWait || got.Criterion != "labels" {
		t.Fatalf("decision = %+v, want wait/labels", got)
	}
}

func TestEvaluateFailedCheckReturnsNeedsAgentFix(t *testing.T) {
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"ci"}}}
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
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"ci"}}}
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
	cfg := Config{RequiredCheckSource: fakeCheckSource{names: []string{"ci"}}}
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

func TestReviewRequiredUsesReviewRule(t *testing.T) {
	p := New(Config{})
	for _, tc := range []struct {
		mode      core.ReviewMode
		approvals int
		want      bool
	}{
		{core.ReviewModeLLM, 0, true},
		{core.ReviewModeHuman, 0, true},
		{core.ReviewModeLLM, 1, false},
		{core.ReviewModeHuman, 1, false},
		{core.ReviewModeNone, 0, false},
	} {
		got := p.ReviewRequired(tc.mode, core.Observation{PRState: "open", Approvals: tc.approvals})
		if got != tc.want {
			t.Errorf("mode=%s approvals=%d ReviewRequired=%v, want %v", tc.mode, tc.approvals, got, tc.want)
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
		SourceChecksRule{Source: fakeCheckSource{names: []string{"ci"}}},
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
		SourceChecksRule{Source: fakeCheckSource{names: []string{"ci"}}},
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

// TestPolicyAcceptsResolvedWorkflowNames covers the names config load feeds
// into the source after a workflow rename: a verify job and a matrix-backed
// test job, with none of the old build/vet/lint jobs present.
func TestPolicyAcceptsResolvedWorkflowNames(t *testing.T) {
	obs := core.Observation{
		PRState: "open",
		Checks: []core.CheckResult{
			{Name: "verify", State: core.CheckSuccess},
			{Name: "test", State: core.CheckSuccess},
		},
	}
	resolved := New(Config{RequiredCheckSource: fakeCheckSource{names: []string{"test", "verify"}}}).Evaluate(obs)
	if resolved.Verdict != core.VerdictMerge {
		t.Fatalf("verdict with resolved names = %+v, want merge", resolved)
	}
	stale := New(Config{RequiredCheckSource: fakeCheckSource{names: []string{"build", "vet", "test", "lint"}}}).Evaluate(obs)
	if stale.Verdict != core.VerdictWait || stale.Criterion != "required_checks" {
		t.Fatalf("verdict with stale names = %+v, want required_checks wait", stale)
	}
}

// TestPolicyHonoursSwappedSources proves the policy follows whichever names a
// source returns with no policy edit between the two evaluations.
func TestPolicyHonoursSwappedSources(t *testing.T) {
	obs := core.Observation{
		PRState: "open",
		Checks: []core.CheckResult{
			{Name: "verify", State: core.CheckSuccess},
			{Name: "test", State: core.CheckSuccess},
		},
	}
	first := New(Config{RequiredCheckSource: fakeCheckSource{names: []string{"verify"}}}).Evaluate(obs)
	if first.Verdict != core.VerdictMerge {
		t.Fatalf("first source decision = %+v, want merge", first)
	}
	second := New(Config{RequiredCheckSource: fakeCheckSource{names: []string{"legacy"}}}).Evaluate(obs)
	if second.Verdict != core.VerdictWait || second.Criterion != "required_checks" {
		t.Fatalf("second source decision = %+v, want required_checks wait", second)
	}
}

func TestPolicySourceErrorFailsClosed(t *testing.T) {
	p := New(Config{RequiredCheckSource: fakeCheckSource{err: errors.New("provider down")}})
	got := p.Evaluate(core.Observation{PRState: "open"})
	if got.Verdict != core.VerdictWait || got.Criterion != "required_checks" {
		t.Fatalf("decision = %+v, want required_checks wait", got)
	}
	if !strings.Contains(got.Reason, "provider down") {
		t.Fatalf("reason = %q, want the source error", got.Reason)
	}
}

func TestPolicyEmptySourceAddsNoGate(t *testing.T) {
	p := New(Config{RequiredCheckSource: fakeCheckSource{}})
	if got := p.Evaluate(core.Observation{PRState: "open"}); got.Verdict != core.VerdictMerge {
		t.Fatalf("decision = %+v, want merge with no required checks", got)
	}
}
