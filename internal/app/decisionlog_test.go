package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type captureDecisionHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureDecisionHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureDecisionHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureDecisionHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureDecisionHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureDecisionHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

func captureDecisions(t *testing.T) *captureDecisionHandler {
	t.Helper()
	h := &captureDecisionHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return h
}

func decisionRecord(t *testing.T, h *captureDecisionHandler, message string) slog.Record {
	t.Helper()
	for _, r := range h.snapshot() {
		if r.Message == message {
			return r
		}
	}
	t.Fatalf("no %q record among %d logs", message, len(h.snapshot()))
	return slog.Record{}
}

func decisionFields(r slog.Record) map[string]any {
	out := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		value := a.Value.Any()
		if n, ok := value.(int64); ok {
			value = int(n)
		}
		out[a.Key] = value
		return true
	})
	return out
}

func requireDecisionFields(t *testing.T, r slog.Record, want map[string]any) {
	t.Helper()
	got := decisionFields(r)
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s: field %s = %#v, want %#v", r.Message, key, got[key], value)
		}
	}
}

func countDecisionRecords(h *captureDecisionHandler, message string) int {
	n := 0
	for _, r := range h.snapshot() {
		if r.Message == message {
			n++
		}
	}
	return n
}

func TestDecisionLogDispatchAndReconcileFields(t *testing.T) {
	h := captureDecisions(t)
	ctx := WithCycleID(context.Background(), "run-1")
	logger := DecisionLogger{}

	logger.logDispatchCandidates(ctx, 7, 3, 4, 2)
	logger.logDispatchSkip(ctx, SkippedTask{
		TaskID:       "T-2",
		Code:         SkipSelectedScopeOverlap,
		BlockedBy:    "T-1",
		BlockedByRun: "run-1",
		Overlap:      []core.ScopePath{core.ScopeFile("a.go"), core.ScopeFile("b.go")},
		Reason:       "scope overlaps T-1",
	})

	run := core.NewRun("run-1", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.ObservedSHA = "old-sha"
	obs := core.Observation{PRNumber: 42, HeadSHA: "new-sha", BaseSHA: "base", Behind: true}
	decision := core.MergeDecision{Verdict: core.VerdictNeedsRebase, Reason: "branch is behind base"}
	logger.logReconcileDecision(ctx, 42, run, obs, decision, reconcileAction(decision))

	for _, r := range h.snapshot() {
		if r.Level != slog.LevelInfo {
			t.Errorf("%s level = %s, want INFO", r.Message, r.Level)
		}
	}

	requireDecisionFields(t, decisionRecord(t, h, "dispatch.candidates"), map[string]any{
		"cycle_id": "run-1", "candidates": 7, "active": 3, "limit": 4, "budget": 2,
	})
	requireDecisionFields(t, decisionRecord(t, h, "dispatch.skip"), map[string]any{
		"cycle_id": "run-1", "task_id": "T-2", "code": string(SkipSelectedScopeOverlap),
		"blocked_by": "T-1", "blocked_by_run": "run-1",
		"overlap": "f:a.go,f:b.go", "reason": "scope overlaps T-1",
	})
	requireDecisionFields(t, decisionRecord(t, h, "reconcile.decision"), map[string]any{
		"cycle_id": "run-1", "pr": 42, "run_id": "run-1", "task_id": "T-1",
		"head_sha": "new-sha", "observed_sha": "old-sha", "behind": true,
		"state": string(core.RunWaitingCI), "verdict": string(core.VerdictNeedsRebase),
		"reason": "branch is behind base", "action": "rebase",
	})
}

func TestDecisionLogTransitionFields(t *testing.T) {
	h := captureDecisions(t)
	ctx := WithCycleID(context.Background(), "run-9")
	logger := DecisionLogger{}

	logger.logTransitionSpawn(ctx, "run-9", "T-9", "beacon/T-9")
	logger.logTransitionWatchPR(ctx, "run-9", 77)
	logger.logTransitionRebase(ctx, "run-9", 77, 2, "sha-new", "rebased")
	logger.logTransitionWake(ctx, "run-9", 77, "woke")
	logger.logTransitionMerge(ctx, "run-9", 77, "attempt")
	logger.logTransitionDeliveryClaim(ctx, "run-9", 77, true)
	logger.logTransitionParentNotify(ctx, "run-9", 77, "notified")

	cases := []struct {
		message string
		want    map[string]any
	}{
		{"transition.spawn", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "task_id": "T-9", "branch": "beacon/T-9"}},
		{"transition.watch_pr", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77}},
		{"transition.rebase", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "attempt": 2, "new_sha": "sha-new", "result": "rebased"}},
		{"transition.wake", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "result": "woke"}},
		{"transition.merge", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "result": "attempt"}},
		{"transition.delivery_claim", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "claimed": true}},
		{"transition.parent_notify", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "result": "notified"}},
	}
	for _, tc := range cases {
		r := decisionRecord(t, h, tc.message)
		if r.Level != slog.LevelInfo {
			t.Errorf("%s level = %s, want INFO", tc.message, r.Level)
		}
		requireDecisionFields(t, r, tc.want)
	}
	if got := len(h.snapshot()); got != len(cases) {
		t.Fatalf("emitted %d records, want %d", got, len(cases))
	}
}

func TestCycleIDContext(t *testing.T) {
	if got := cycleID(context.Background()); got != "" {
		t.Fatalf("empty context cycle id = %q", got)
	}
	ctx := WithCycleID(context.Background(), "run-x")
	if got := cycleID(ctx); got != "run-x" {
		t.Fatalf("cycle id = %q, want run-x", got)
	}
	parentCtx := WithParentSession(ctx, "parent-session")
	if got := parentSession(parentCtx); got != "parent-session" {
		t.Fatalf("parent session = %q, want parent-session", got)
	}
	if got := cycleID(parentCtx); got != "run-x" {
		t.Fatalf("cycle id after WithParentSession = %q, want run-x", got)
	}
}

func TestShortLogReason(t *testing.T) {
	if got := shortLogReason("short reason"); got != "short reason" {
		t.Fatalf("shortLogReason = %q", got)
	}
	if got := shortLogReason("a\nb\rc"); got != "a b c" {
		t.Fatalf("shortLogReason = %q, want a b c", got)
	}
	long := strings.Repeat("x", maxLogReasonRunes+40)
	want := strings.Repeat("x", maxLogReasonRunes) + "..."
	if got := shortLogReason("  " + long + "  "); got != want {
		t.Fatalf("long reason = %q, want %q", got, want)
	}
}

func TestDispatchEmitsDecisionLogs(t *testing.T) {
	h := captureDecisions(t)
	store := newFakeStore()
	tasks := []core.Task{
		{ID: "T-A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
		{ID: "T-B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("b.go")}},
		{ID: "T-C", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("active.go")}},
	}
	active := core.NewRun("r-active", "T-Z", time.Now().UTC())
	active.State = core.RunRunning
	active.Scope = []core.ScopePath{core.ScopeFile("active.go")}
	if err := store.Save(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		Source: &fakeSource{tasks: tasks}, Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 4, MaxActiveAgents: 2},
	}
	if _, err := d.Dispatch(context.Background(), 4); err != nil {
		t.Fatal(err)
	}

	requireDecisionFields(t, decisionRecord(t, h, "dispatch.candidates"), map[string]any{
		"candidates": 3, "active": 1, "limit": 3, "budget": 1,
	})
	if got := countDecisionRecords(h, "dispatch.skip"); got != 1 {
		t.Fatalf("dispatch.skip count = %d, want 1", got)
	}
	requireDecisionFields(t, decisionRecord(t, h, "dispatch.skip"), map[string]any{
		"task_id": "T-C", "code": string(SkipActiveScopeOverlap), "blocked_by_run": "r-active",
	})
	spawn := decisionRecord(t, h, "transition.spawn")
	fields := decisionFields(spawn)
	cycle, _ := fields["cycle_id"].(string)
	runID, _ := fields["run_id"].(string)
	if cycle == "" || cycle != runID {
		t.Fatalf("spawn correlation cycle_id = %q, run_id = %q", cycle, runID)
	}
}

func TestReconcileEmitsOneDecisionWithRunCorrelation(t *testing.T) {
	h := captureDecisions(t)
	r, _, _, _, _ := newReconciler(t, core.VerdictNeedsRebase,
		core.Observation{PRNumber: 42, HeadSHA: "sha2", BaseSHA: "base-1", PRState: "open", Behind: true})
	if _, err := r.Reconcile(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if got := countDecisionRecords(h, "reconcile.decision"); got != 1 {
		t.Fatalf("reconcile.decision count = %d, want 1", got)
	}
	requireDecisionFields(t, decisionRecord(t, h, "reconcile.decision"), map[string]any{
		"cycle_id": "r1", "run_id": "r1", "task_id": "T-1", "pr": 42,
		"verdict": string(core.VerdictNeedsRebase), "action": "rebase",
	})
	requireDecisionFields(t, decisionRecord(t, h, "transition.rebase"), map[string]any{
		"cycle_id": "r1", "run_id": "r1", "pr": 42,
		"attempt": 1, "new_sha": "new-sha2", "result": "rebased",
	})
}
