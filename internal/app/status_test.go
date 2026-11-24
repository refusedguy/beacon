package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type stubTickReconciler struct {
	calls    int
	pr       int
	decision core.MergeDecision
	err      error
	panicVal any
}

func (s *stubTickReconciler) Reconcile(_ context.Context, pr int) (core.MergeDecision, error) {
	s.calls++
	s.pr = pr
	if s.panicVal != nil {
		panic(s.panicVal)
	}
	return s.decision, s.err
}

func waitingRun(pr int) *core.Run {
	run := core.NewRun("run-reconcile", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = pr
	return run
}

func TestDeriveDurationsTicksLiveRunUntilNow(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-live", "T-LIVE", now.Add(-10*time.Minute))
	run.State = core.RunWaitingCI
	run.UpdatedAt = now.Add(-5 * time.Minute)
	events := []RunEvent{{State: string(core.RunWaitingCI), At: run.UpdatedAt}}

	got := deriveDurations(run, events, now)
	if got.WaitingCI != 300 {
		t.Fatalf("waiting CI = %ds, want 300s", got.WaitingCI)
	}
	if got.Running != 0 || got.Blocked != 0 {
		t.Fatalf("live durations = %+v, want only waiting CI", got)
	}
}

func TestDeriveDurationsFreezesTerminalRunAtUpdatedAt(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-done", "T-DONE", now.Add(-10*time.Minute))
	run.State = core.RunMerged
	run.UpdatedAt = now.Add(-5 * time.Minute)
	events := []RunEvent{{State: string(core.RunRunning), At: now.Add(-9 * time.Minute)}}

	got := deriveDurations(run, events, now)
	if got.Running != 240 {
		t.Fatalf("terminal running = %ds, want 240s", got.Running)
	}
	if got.WaitingCI != 0 || got.Blocked != 0 {
		t.Fatalf("terminal durations = %+v, want zero", got)
	}
}

func TestReconcileServiceTickReturnsDecisionAndState(t *testing.T) {
	store := newFakeStore()
	if err := store.Save(context.Background(), waitingRun(42)); err != nil {
		t.Fatal(err)
	}
	ticker := &stubTickReconciler{decision: core.MergeDecision{
		Verdict:   core.VerdictWait,
		Reason:    "waiting for checks",
		Criterion: "approvals",
	}}
	svc := ReconcileService{Reconciler: ticker, Store: store}

	got := svc.Tick(context.Background(), 42)
	if ticker.calls != 1 || ticker.pr != 42 {
		t.Fatalf("reconcile calls = %d, pr = %d", ticker.calls, ticker.pr)
	}
	if got.PR != 42 || got.Decision.Verdict != "wait" ||
		got.Decision.Reason != "waiting for checks" || got.Decision.Criterion != "approvals" {
		t.Fatalf("result = %+v", got)
	}
	if got.State != string(core.RunWaitingCI) {
		t.Fatalf("state = %q, want waiting_ci", got.State)
	}
	if got.Error != "" {
		t.Fatalf("unexpected error = %q", got.Error)
	}
}

func TestReconcileServiceTickCapturesError(t *testing.T) {
	ticker := &stubTickReconciler{err: errors.New("scm unavailable")}
	svc := ReconcileService{Reconciler: ticker}

	got := svc.Tick(context.Background(), 7)
	if got.Error != "scm unavailable" {
		t.Fatalf("error = %q", got.Error)
	}
	if ticker.calls != 1 {
		t.Fatalf("reconcile calls = %d", ticker.calls)
	}
}

func TestReconcileServiceTickRecoversPanic(t *testing.T) {
	ticker := &stubTickReconciler{panicVal: "boom"}
	svc := ReconcileService{Reconciler: ticker}

	got := svc.Tick(context.Background(), 9)
	if !strings.Contains(got.Error, "boom") {
		t.Fatalf("error = %q, want panic text", got.Error)
	}
	if ticker.calls != 1 {
		t.Fatalf("reconcile calls = %d", ticker.calls)
	}
}

func TestReconcileServiceTickRejectsInvalidInput(t *testing.T) {
	ticker := &stubTickReconciler{}
	svc := ReconcileService{Reconciler: ticker}

	for _, pr := range []int{0, -1} {
		got := svc.Tick(context.Background(), pr)
		if got.Error != "pr must be positive" {
			t.Fatalf("pr %d: error = %q", pr, got.Error)
		}
	}
	if ticker.calls != 0 {
		t.Fatalf("invalid input still reconciled %d times", ticker.calls)
	}
}

func TestReconcileServiceTickWithoutReconciler(t *testing.T) {
	got := (&ReconcileService{}).Tick(context.Background(), 5)
	if got.Error != "reconciler is not configured" {
		t.Fatalf("error = %q", got.Error)
	}
}

func TestMakeRunSnapshotMapsChecksWithoutSCM(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	start := now.Add(-time.Minute)
	run := core.NewRun("run-checks", "T-CHECK", now)
	run.Checks = []core.CheckResult{
		{Name: "build", State: core.CheckSuccess, StartedAt: start, CompletedAt: start.Add(28 * time.Second)},
		{Name: "lint", State: core.CheckPending},
	}
	got := makeRunSnapshot(run, nil, nil, now)
	if len(got.Checks) != 2 {
		t.Fatalf("checks = %+v", got.Checks)
	}
	if got.Checks[0].Name != "build" || got.Checks[0].State != "success" || got.Checks[0].DurationSeconds != 28 {
		t.Fatalf("first check = %+v", got.Checks[0])
	}
	if got.Checks[1].Name != "lint" || got.Checks[1].State != "pending" || got.Checks[1].DurationSeconds != 0 {
		t.Fatalf("second check = %+v", got.Checks[1])
	}
}

func TestRunSnapshotJSONChecksContract(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-checks", "T-CHECK", now)
	got := makeRunSnapshot(run, nil, nil, now)
	if got.Checks == nil || len(got.Checks) != 0 {
		t.Fatalf("old run checks = %+v, want empty array", got.Checks)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Checks []struct {
			Name            string `json:"name"`
			State           string `json:"state"`
			DurationSeconds int64  `json:"duration_seconds"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Checks == nil || len(decoded.Checks) != 0 {
		t.Fatalf("json checks = %+v, want []", decoded.Checks)
	}

	run.Checks = []core.CheckResult{{Name: "ci", State: core.CheckFailure}}
	b, err = json.Marshal(makeRunSnapshot(run, nil, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Checks) != 1 || decoded.Checks[0].Name != "ci" ||
		decoded.Checks[0].State != "failure" || decoded.Checks[0].DurationSeconds != 0 {
		t.Fatalf("json checks = %+v", decoded.Checks)
	}
}
