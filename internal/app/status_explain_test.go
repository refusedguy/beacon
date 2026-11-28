package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type explainStoreStub struct {
	data *RunExplainData
	err  error
}

func (s explainStoreStub) RunExplain(context.Context, core.RunID) (*RunExplainData, error) {
	return s.data, s.err
}

func explainRun(state core.RunState, at time.Time) *core.Run {
	run := core.NewRun("run-explain", "T-EXPLAIN", at.Add(-time.Hour))
	run.State = state
	run.UpdatedAt = at.Add(-2 * time.Minute)
	run.Reason = "operator note"
	return run
}

func TestExplainRunTextCoversEveryState(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		state  core.RunState
		action string
	}{
		{core.RunPending, "none"},
		{core.RunRunning, "watch"},
		{core.RunWaitingCI, "watch"},
		{core.RunNeedsRebase, "watch"},
		{core.RunNeedsAgentFix, "nudge"},
		{core.RunBlocked, "unblock"},
		{core.RunMerged, "none"},
		{core.RunClosed, "retry"},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			run := explainRun(tc.state, now)
			events := []RunEvent{{Kind: "state", State: string(tc.state), At: run.UpdatedAt}}
			got := ExplainRunText(run, events, nil, now)
			want := "action: " + tc.action
			if !strings.Contains(got, want) {
				t.Fatalf("text %q missing %q", got, want)
			}
			if !strings.Contains(got, "state") || !strings.Contains(got, "run run-explain") {
				t.Fatalf("text %q missing run or state", got)
			}
		})
	}
}

func TestExplainRunTextReportsFactsTogether(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	run := explainRun(core.RunNeedsAgentFix, now)
	run.UpdatedAt = now.Add(-5 * time.Minute)
	events := []RunEvent{{ID: 1, Kind: "checks_failed", State: string(core.RunWaitingCI), At: now.Add(-9 * time.Minute)},
		{ID: 2, Kind: "checks_failed", State: string(core.RunNeedsAgentFix), At: now.Add(-5 * time.Minute)}}
	decision := &Decision{At: now.Add(-5 * time.Minute), Verdict: string(core.VerdictNeedsAgentFix),
		Reason: "unit failed", Fields: map[string]any{"criterion": "required_checks"}}
	run.Checks = []core.CheckResult{{Name: "unit", State: core.CheckFailure}}
	run.FixAttempts = 1

	got := ExplainRunText(run, events, decision, now)
	for _, want := range []string{
		"is needs_agent_fix for 5m0s",
		"last transition: checks_failed -> needs_agent_fix",
		"last verdict: needs_agent_fix criterion: required_checks",
		"failed check: unit",
		"attempts: rebase 0/3, fix 1/3",
		"action: nudge",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text %q missing %q", got, want)
		}
	}
}

func TestExplainRunTextMarksStaleReason(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	run := explainRun(core.RunWaitingCI, now)
	run.Reason = "checks green"
	decision := &Decision{At: now, Verdict: string(core.VerdictWait),
		Reason: "need 2 approvals, have 0", Fields: map[string]any{"criterion": "approvals"}}

	got := ExplainRunText(run, nil, decision, now)
	for _, want := range []string{"stale", "need 2 approvals, have 0", "action: approve"} {
		if !strings.Contains(got, want) {
			t.Errorf("text %q missing %q", got, want)
		}
	}
}

func TestExplainRunTextMarksExhaustedAttempts(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	run := explainRun(core.RunNeedsAgentFix, now)
	run.FixAttempts = 3

	got := ExplainRunText(run, nil, nil, now)
	if !strings.Contains(got, "fix 3/3 (exhausted)") {
		t.Fatalf("text %q missing exhausted attempts", got)
	}
}

func TestExplainServiceRejectsUnknownRun(t *testing.T) {
	svc := StatusExplainService{Store: explainStoreStub{}}
	_, err := svc.Explain(context.Background(), "run-missing", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "run-missing") {
		t.Fatalf("error = %v, want not-found for run-missing", err)
	}
}

func TestExplainServiceRendersStoredData(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	run := explainRun(core.RunBlocked, now)
	store := explainStoreStub{data: &RunExplainData{
		Run:      run,
		Events:   []RunEvent{{Kind: "block", State: string(core.RunBlocked), At: now.Add(-time.Minute)}},
		Decision: &Decision{Verdict: string(core.VerdictBlocked), Reason: "base red", Fields: map[string]any{"criterion": "invariant"}},
	}}
	got, err := (StatusExplainService{Store: store}).Explain(context.Background(), run.ID, now)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.Contains(got, "action: unblock") {
		t.Fatalf("text %q missing unblock", got)
	}
}
