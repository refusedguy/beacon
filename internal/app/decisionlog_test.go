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

func TestDecisionLogTransitionDecisionFields(t *testing.T) {
	sink := &memoryDecisionSink{}
	logger := DecisionLogger{Sink: sink}
	ctx := WithCycleID(context.Background(), "run-9")

	logger.logTransitionWatchPR(ctx, "run-9", 77)
	logger.Wait()
	logger.logTransitionWake(ctx, "run-9", 77, "woke")
	logger.logTransitionMerge(ctx, "run-9", 77, "attempt")
	logger.logTransitionParentNotify(ctx, "run-9", 77, "notified")

	want := []Decision{
		{CycleID: "run-9", Kind: "transition.watch_pr", RunID: "run-9", PRNumber: 77},
		{CycleID: "run-9", Kind: "transition.wake", RunID: "run-9", PRNumber: 77, Action: "woke"},
		{CycleID: "run-9", Kind: "transition.merge", RunID: "run-9", PRNumber: 77, Action: "attempt"},
		{CycleID: "run-9", Kind: "transition.parent_notify", RunID: "run-9", PRNumber: 77, Action: "notified"},
	}
	if len(sink.decisions) != len(want) {
		t.Fatalf("recorded %d decisions, want %d", len(sink.decisions), len(want))
	}
	for i, d := range sink.decisions {
		w := want[i]
		if d.CycleID != w.CycleID || d.Kind != w.Kind || d.RunID != w.RunID || d.PRNumber != w.PRNumber || d.Action != w.Action {
			t.Errorf("decision %d = %+v, want %+v", i, d, w)
		}
		if len(d.Fields) != 0 {
			t.Errorf("decision %d fields = %+v, want none", i, d.Fields)
		}
	}
}

func TestDecisionLogTransitionExtraDecisionFields(t *testing.T) {
	h := captureDecisions(t)
	sink := &memoryDecisionSink{}
	logger := DecisionLogger{Sink: sink}
	ctx := WithCycleID(context.Background(), "run-9")

	logger.logTransitionSpawn(ctx, "run-9", "T-9", "beacon/T-9")
	logger.Wait()
	logger.logTransitionRebase(ctx, "run-9", 77, 2, "sha-new", "rebased")
	logger.logTransitionDeliveryClaim(ctx, "run-9", 77, true)

	logWant := []struct {
		message string
		fields  map[string]any
	}{
		{"transition.spawn", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "task_id": "T-9", "branch": "beacon/T-9"}},
		{"transition.rebase", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "attempt": 2, "new_sha": "sha-new", "result": "rebased"}},
		{"transition.delivery_claim", map[string]any{"cycle_id": "run-9", "run_id": "run-9", "pr": 77, "claimed": true}},
	}
	for _, w := range logWant {
		got := decisionFields(decisionRecord(t, h, w.message))
		if len(got) != len(w.fields) {
			t.Errorf("%s log fields = %+v, want %+v", w.message, got, w.fields)
			continue
		}
		for key, value := range w.fields {
			if got[key] != value {
				t.Errorf("%s log field %s = %#v, want %#v", w.message, key, got[key], value)
			}
		}
	}

	want := []struct {
		kind   string
		taskID string
		pr     int
		action string
		fields map[string]any
	}{
		{"transition.spawn", "T-9", 0, "", map[string]any{"branch": "beacon/T-9"}},
		{"transition.rebase", "", 77, "rebased", map[string]any{"attempt": 2, "new_sha": "sha-new"}},
		{"transition.delivery_claim", "", 77, "", map[string]any{"claimed": true}},
	}
	if len(sink.decisions) != len(want) {
		t.Fatalf("recorded %d decisions, want %d", len(sink.decisions), len(want))
	}
	for i, d := range sink.decisions {
		w := want[i]
		if d.Kind != w.kind || d.TaskID != w.taskID || d.PRNumber != w.pr || d.Action != w.action {
			t.Errorf("decision %d = %+v, want kind=%q task=%q pr=%d action=%q", i, d, w.kind, w.taskID, w.pr, w.action)
		}
		if len(d.Fields) != len(w.fields) {
			t.Errorf("decision %d fields = %+v, want %+v", i, d.Fields, w.fields)
			continue
		}
		for key, value := range w.fields {
			if d.Fields[key] != value {
				t.Errorf("decision %d field %s = %#v, want %#v", i, key, d.Fields[key], value)
			}
		}
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

// countingDecisionSink counts store touches so tests can prove the reconcile
// cache short-circuits both the read and the write for an unchanged decision.
type countingDecisionSink struct {
	mu        sync.Mutex
	decisions []Decision
	reads     int
	writes    int
}

func (s *countingDecisionSink) RecordDecision(_ context.Context, d Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = append(s.decisions, d)
	s.writes++
	return nil
}

func (s *countingDecisionSink) LastDecision(_ context.Context, runID string) (*Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	for i := len(s.decisions) - 1; i >= 0; i-- {
		if s.decisions[i].RunID == runID {
			d := s.decisions[i]
			return &d, nil
		}
	}
	return nil, nil
}

func (s *countingDecisionSink) counts() (reads, writes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads, s.writes
}

func TestReconcileDecisionCacheSkipsStoreTouches(t *testing.T) {
	ctx := context.Background()
	sink := &countingDecisionSink{}
	logger := DecisionLogger{Sink: sink}
	run := makeRun("r-cache", 42, core.RunWaitingCI)
	obs := core.Observation{HeadSHA: "sha1", PRNumber: 42}
	decision := core.MergeDecision{Verdict: core.VerdictWait, Reason: "checks pending"}
	emit := func() { logger.logReconcileDecision(ctx, 42, run, obs, decision, "wait") }

	emit()
	reads, writes := sink.counts()
	if reads != 1 || writes != 1 {
		t.Fatalf("after first decision reads=%d writes=%d, want 1/1", reads, writes)
	}

	for tick := 0; tick < 10; tick++ {
		for i := 0; i < 50; i++ {
			emit()
		}
	}
	reads, writes = sink.counts()
	if reads != 1 || writes != 1 {
		t.Fatalf("after 500 unchanged decisions reads=%d writes=%d, want 1/1", reads, writes)
	}
}

func TestReconcileDecisionCacheWritesAfterChange(t *testing.T) {
	ctx := context.Background()
	sink := &countingDecisionSink{}
	logger := DecisionLogger{Sink: sink}
	run := makeRun("r-change", 42, core.RunWaitingCI)
	decision := core.MergeDecision{Verdict: core.VerdictWait, Reason: "checks pending"}

	logger.logReconcileDecision(ctx, 42, run, core.Observation{HeadSHA: "sha1", PRNumber: 42}, decision, "wait")
	logger.logReconcileDecision(ctx, 42, run, core.Observation{HeadSHA: "sha2", PRNumber: 42}, decision, "wait")

	reads, writes := sink.counts()
	if reads != 2 || writes != 2 {
		t.Fatalf("after head change reads=%d writes=%d, want 2/2", reads, writes)
	}
	if got := sink.decisions[1].Fields["head_sha"]; got != "sha2" {
		t.Fatalf("second decision head_sha = %v, want sha2", got)
	}
}

// gatedDecisionSink blocks every write until the gate opens. It proves a
// dispatch does not wait on the audit sink before returning to its caller.
type gatedDecisionSink struct {
	gate      <-chan struct{}
	mu        sync.Mutex
	decisions []Decision
}

func (s *gatedDecisionSink) RecordDecision(_ context.Context, d Decision) error {
	<-s.gate
	s.mu.Lock()
	s.decisions = append(s.decisions, d)
	s.mu.Unlock()
	return nil
}

func (s *gatedDecisionSink) snapshot() []Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Decision(nil), s.decisions...)
}

func TestDispatchDecisionWritesAreDeferred(t *testing.T) {
	gate := make(chan struct{})
	sink := &gatedDecisionSink{gate: gate}
	d := &Dispatcher{
		Source: &fakeSource{tasks: []core.Task{
			{ID: "T-A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
			{ID: "T-B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("b.go")}},
		}},
		Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: newFakeStore(),
		Concurrency: core.Concurrency{MaxInFlight: 2, MaxActiveAgents: 2},
		Decisions:   DecisionLogger{Sink: sink},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := d.Dispatch(context.Background(), 2); err != nil {
			t.Errorf("Dispatch: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch blocked on the decision sink")
	}
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("sink has %d rows before the gate opens, want 0", got)
	}

	close(gate)
	d.Decisions.Wait()
	got := sink.snapshot()
	if len(got) != 3 {
		t.Fatalf("recorded %d decisions, want 3: %+v", len(got), got)
	}
	if got[0].Kind != "dispatch.candidates" || got[1].Kind != "transition.spawn" || got[2].Kind != "transition.spawn" {
		t.Fatalf("decision kinds = %q, %q, %q", got[0].Kind, got[1].Kind, got[2].Kind)
	}
}

func TestDeferredDispatchDecisionsKeepRowsAndCycleIDs(t *testing.T) {
	sink := &countingDecisionSink{}
	store := newFakeStore()
	active := core.NewRun("r-active", "T-Z", time.Now().UTC())
	active.State = core.RunRunning
	active.Scope = []core.ScopePath{core.ScopeFile("active.go")}
	if err := store.Save(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		Source: &fakeSource{tasks: []core.Task{
			{ID: "T-A", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("a.go")}},
			{ID: "T-B", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("b.go")}},
			{ID: "T-C", Status: core.TaskAvailable, Scope: []core.ScopePath{core.ScopeFile("active.go")}},
		}},
		Planner: NewPlanner(nil), Workspace: &fakeWorkspace{},
		Harness: &fakeHarness{}, Store: store,
		Concurrency: core.Concurrency{MaxInFlight: 4, MaxActiveAgents: 4},
		Decisions:   DecisionLogger{Sink: sink},
	}

	if _, err := d.Dispatch(WithCycleID(context.Background(), "cycle-1"), 4); err != nil {
		t.Fatal(err)
	}
	d.Decisions.Wait()
	got := sink.decisions
	if len(got) != 4 {
		t.Fatalf("recorded %d decisions, want 4: %+v", len(got), got)
	}
	if got[0].Kind != "dispatch.candidates" || got[0].CycleID != "cycle-1" {
		t.Errorf("first decision = %+v, want dispatch.candidates in cycle-1", got[0])
	}
	if got[1].Kind != "dispatch.skip" || got[1].CycleID != "cycle-1" || got[1].TaskID != "T-C" {
		t.Errorf("second decision = %+v, want dispatch.skip for T-C in cycle-1", got[1])
	}
	for i := 2; i < len(got); i++ {
		if got[i].Kind != "transition.spawn" {
			t.Errorf("decision %d kind = %q, want transition.spawn", i, got[i].Kind)
		}
		if got[i].CycleID != got[i].RunID || got[i].RunID == "" {
			t.Errorf("decision %d cycle_id = %q, run_id = %q, want equal and non-empty", i, got[i].CycleID, got[i].RunID)
		}
	}
}

func TestReconcileRepeatedCallsSkipDuplicateDecisionWrites(t *testing.T) {
	ctx := context.Background()
	sink := &countingDecisionSink{}
	r, _, _, _, _ := newReconciler(t, core.VerdictNoop, core.Observation{
		PRNumber: 42, HeadSHA: "sha2", PRState: "open",
	})
	r.Decisions = DecisionLogger{Sink: sink}

	if _, err := r.Reconcile(ctx, 42); err != nil {
		t.Fatal(err)
	}
	reads, writes := sink.counts()
	if reads != 1 || writes != 1 {
		t.Fatalf("after first reconcile reads=%d writes=%d, want 1/1", reads, writes)
	}

	for tick := 0; tick < 10; tick++ {
		for i := 0; i < 50; i++ {
			if _, err := r.Reconcile(ctx, 42); err != nil {
				t.Fatal(err)
			}
		}
	}
	reads, writes = sink.counts()
	if reads != 1 || writes != 1 {
		t.Fatalf("after 500 unchanged reconciles reads=%d writes=%d, want 1/1", reads, writes)
	}
}
