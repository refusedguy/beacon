package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type stubTickReconciler struct {
	pr       int
	decision core.MergeDecision
	err      error
	panicVal any
}

func (s *stubTickReconciler) Reconcile(_ context.Context, pr int) (core.MergeDecision, error) {
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
		t.Fatalf(`store.Save(context.Background(), waitingRun(42)): %v`, err)
	}
	ticker := &stubTickReconciler{decision: core.MergeDecision{
		Verdict:   core.VerdictWait,
		Reason:    "waiting for checks",
		Criterion: "approvals",
	}}
	svc := ReconcileService{Reconciler: ticker, Store: store}

	got := svc.Tick(context.Background(), 42)
	if ticker.pr != 42 {
		t.Fatalf("reconcile pr = %d, want 42", ticker.pr)
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
}

func TestReconcileServiceTickRecoversPanic(t *testing.T) {
	ticker := &stubTickReconciler{panicVal: "boom"}
	svc := ReconcileService{Reconciler: ticker}

	got := svc.Tick(context.Background(), 9)
	if !strings.Contains(got.Error, "boom") {
		t.Fatalf("error = %q, want panic text", got.Error)
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

func TestRunSnapshotCarriesCIWallSeconds(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-wall", "T-WALL", now)
	run.Checks = []core.CheckResult{
		{Name: "lint", StartedAt: now, CompletedAt: now.Add(20 * time.Second)},
		{Name: "test", StartedAt: now.Add(15 * time.Second), CompletedAt: now.Add(time.Minute)},
	}
	got := makeRunSnapshot(run, nil, nil, now)
	if got.CIWallSeconds != 60 {
		t.Fatalf("ci wall seconds = %d, want 60", got.CIWallSeconds)
	}
	if got.Checks[0].DurationSeconds != 20 || got.Checks[1].DurationSeconds != 45 {
		t.Fatalf("check durations changed: %+v", got.Checks)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf(`json.Marshal(got): %v`, err)
	}
	var decoded struct {
		CIWallSeconds int64 `json:"ci_wall_seconds"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf(`json.Unmarshal(b, &decoded): %v`, err)
	}
	if decoded.CIWallSeconds != 60 {
		t.Fatalf("json ci_wall_seconds = %d, want 60", decoded.CIWallSeconds)
	}
}

func TestRunSnapshotReportsStagedJobCriticalPath(t *testing.T) {
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	at := func(second int) time.Time { return now.Add(time.Duration(second) * time.Second) }
	run := core.NewRun("run-critical", "T-CRITICAL", now)
	run.Checks = []core.CheckResult{
		{Name: "build", State: core.CheckSuccess, StartedAt: at(0), CompletedAt: at(30)},
		{Name: "rebase", State: core.CheckSuccess, StartedAt: at(35), CompletedAt: at(90)},
		{Name: "fix", State: core.CheckSuccess, StartedAt: at(95), CompletedAt: at(420)},
	}
	got := makeRunSnapshot(run, nil, nil, now)
	if got.CIWallSeconds != 420 {
		t.Fatalf("ci wall seconds = %d, want 420", got.CIWallSeconds)
	}
	if got.CICriticalPath != 390 {
		t.Fatalf("critical path = %d, want 390", got.CICriticalPath)
	}
	want := []int64{30, 55, 325}
	for i, check := range got.Checks {
		if check.DurationSeconds != want[i] {
			t.Errorf("%s duration = %d, want %d", check.Name, check.DurationSeconds, want[i])
		}
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf(`json.Marshal(got): %v`, err)
	}
	var decoded struct {
		CriticalPath int64 `json:"ci_critical_path_seconds"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf(`json.Unmarshal(b, &decoded): %v`, err)
	}
	if decoded.CriticalPath != 390 {
		t.Fatalf("json critical path = %d, want 390", decoded.CriticalPath)
	}
}

func TestDeriveDurationsSplitsCIWaitByCycle(t *testing.T) {
	now := time.Date(2026, 11, 26, 8, 10, 0, 0, time.UTC)
	at := func(minute int) time.Time { return now.Add(time.Duration(minute-10) * time.Minute) }
	run := core.NewRun("run-cycles", "T-CYCLES", at(0))
	run.State = core.RunMerged
	run.UpdatedAt = at(10)
	events := []RunEvent{
		{State: string(core.RunPending), At: at(0)},
		{State: string(core.RunRunning), At: at(1)},
		{State: string(core.RunWaitingCI), At: at(2)},
		{State: string(core.RunNeedsRebase), At: at(3)},
		{State: string(core.RunRunning), At: at(4)},
		{State: string(core.RunWaitingCI), At: at(5)},
		{State: string(core.RunNeedsAgentFix), At: at(6)},
		{State: string(core.RunRunning), At: at(7)},
		{State: string(core.RunWaitingCI), At: at(8)},
		{State: string(core.RunMerged), At: at(9)},
	}

	got := deriveDurations(run, events, now)
	if got.WaitingCI != 180 {
		t.Fatalf("waiting CI = %d, want 180", got.WaitingCI)
	}
	if got.WaitingCIInitial != 60 || got.WaitingCIRebase != 60 || got.WaitingCIFix != 60 {
		t.Fatalf("cycle split = %+v, want 60/60/60", got)
	}
	if got.WaitingCIInitial+got.WaitingCIRebase+got.WaitingCIFix != got.WaitingCI {
		t.Fatalf("cycle split does not add up: %+v", got)
	}
}

func TestDeriveDurationsFallbackSplitsCIWaitByCycle(t *testing.T) {
	now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-fallback", "T-FALLBACK", now.Add(-2*time.Minute))
	run.State = core.RunWaitingCI
	run.UpdatedAt = now

	got := deriveDurations(run, nil, now)
	if got.WaitingCI != 120 {
		t.Fatalf("waiting CI = %d, want 120", got.WaitingCI)
	}
	if got.WaitingCIInitial != 120 || got.WaitingCIRebase != 0 || got.WaitingCIFix != 0 {
		t.Fatalf("cycle split = %+v, want 120/0/0", got)
	}
	if got.WaitingCIInitial+got.WaitingCIRebase+got.WaitingCIFix != got.WaitingCI {
		t.Fatalf("cycle split does not add up: %+v", got)
	}
}

func TestSnapshotAggregatesCICycles(t *testing.T) {
	now := time.Date(2026, 11, 26, 8, 2, 0, 0, time.UTC)
	run := core.NewRun("run-metrics", "T-METRICS", now.Add(-2*time.Minute))
	run.State = core.RunMerged
	run.UpdatedAt = now
	events := []RunEvent{
		{State: string(core.RunWaitingCI), At: now.Add(-2 * time.Minute)},
		{State: string(core.RunNeedsRebase), At: now.Add(-90 * time.Second)},
		{State: string(core.RunWaitingCI), At: now.Add(-60 * time.Second)},
		{State: string(core.RunMerged), At: now.Add(-30 * time.Second)},
	}
	snap := makeRunSnapshot(run, map[string][]RunEvent{string(run.ID): events}, nil, now)
	m := makeMetrics([]RunSnapshot{snap}, nil, nil, nil, nil)
	if m.CIWaitInitial != 30 || m.CIWaitRebase != 30 || m.CIWaitFix != 0 {
		t.Fatalf("cycle metrics = %d/%d/%d, want 30/30/0", m.CIWaitInitial, m.CIWaitRebase, m.CIWaitFix)
	}
	if m.CIWaitInitial+m.CIWaitRebase+m.CIWaitFix != m.CIWaitSeconds {
		t.Fatalf("cycle metrics do not sum to CI wait: %+v", m)
	}
}

func TestRunSnapshotCarriesSessionIncidentAndPRURL(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-fields", "T-FIELDS", now)
	run.State = core.RunBlocked
	run.PRNumber = 42
	run.SessionID = "sess-fields"
	run.IsFixer = true
	run.BlockedIncidentID = "inc-7"

	got := makeRunSnapshot(run, nil, nil, now, "owner/repo")
	if got.SessionID != "sess-fields" || !got.IsFixer || got.BlockedIncidentID != "inc-7" {
		t.Fatalf("run fields = %+v", got)
	}
	if got.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("pr url = %q", got.PRURL)
	}
	if link := prLink("owner/repo", 0); link != "" {
		t.Fatalf("url without PR = %q", link)
	}
	if link := prLink("https://github.com/owner/repo", 42); link != "" {
		t.Fatalf("url for non owner/repo = %q", link)
	}
}

type snapshotStubStore struct {
	active   []*core.Run
	recent   []*core.Run
	queue    []QueueTask
	incident *core.BaseIncident
}

func (s *snapshotStubStore) ListActive(context.Context) ([]*core.Run, error) { return s.active, nil }
func (s *snapshotStubStore) ListRecent(context.Context, int) ([]*core.Run, error) {
	return s.recent, nil
}
func (s *snapshotStubStore) ListRunEvents(context.Context, []string) ([]RunEvent, error) {
	return nil, nil
}
func (s *snapshotStubStore) ListDecisions(context.Context, []string, int, int) ([]Decision, error) {
	return nil, nil
}
func (s *snapshotStubStore) DecisionCounts(context.Context, []string) ([]DecisionCount, error) {
	return nil, nil
}
func (s *snapshotStubStore) ListQueueTasks(context.Context) ([]QueueTask, error) {
	return s.queue, nil
}
func (s *snapshotStubStore) CurrentIncident(context.Context) (*core.BaseIncident, error) {
	return s.incident, nil
}

func TestSnapshotCarriesQueueIncidentAndDaemon(t *testing.T) {
	ctx := context.Background()
	run := core.NewRun("run-snap", "T-SNAP", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = 9
	store := &snapshotStubStore{
		active: []*core.Run{run},
		queue: []QueueTask{
			{ID: "T-1", Title: "ready card", Priority: 5},
			{ID: "T-2", Title: "blocked card", Priority: 3, DependsOn: []string{"T-1"}, Waiting: true},
		},
		incident: &core.BaseIncident{
			ID: "inc-1", BaseBranch: "main", FailedChecksJSON: `[{"name":"build"},{"name":"lint"}]`,
			FixerRunID: "run-fixer", Attempts: 2,
		},
	}
	daemon := &DaemonSnapshot{State: "alive", PID: 123, HeartbeatAgeSeconds: 1.5}
	svc := SnapshotService{Store: store, Repo: "owner/repo", Daemon: daemon}

	snap, err := svc.Snapshot(ctx)
	if err != nil {
		t.Fatalf(`svc.Snapshot(ctx): %v`, err)
	}
	if snap.Queue == nil || len(snap.Queue) != 2 {
		t.Fatalf("queue = %+v, want two items", snap.Queue)
	}
	if snap.Queue[0].Status != "ready" || snap.Queue[0].DependsOn == nil {
		t.Fatalf("first queue item = %+v", snap.Queue[0])
	}
	if snap.Queue[1].Status != "waiting_deps" || len(snap.Queue[1].DependsOn) != 1 {
		t.Fatalf("second queue item = %+v", snap.Queue[1])
	}
	if snap.BaseIncident == nil || snap.BaseIncident.ID != "inc-1" ||
		snap.BaseIncident.BaseBranch != "main" || len(snap.BaseIncident.FailedChecks) != 2 ||
		snap.BaseIncident.FixerRunID != "run-fixer" || snap.BaseIncident.Attempts != 2 {
		t.Fatalf("base incident = %+v", snap.BaseIncident)
	}
	if snap.Daemon != daemon || snap.Daemon.State != "alive" || snap.Daemon.PID != 123 {
		t.Fatalf("daemon = %+v", snap.Daemon)
	}
	if len(snap.Active) != 1 || snap.Active[0].PRURL != "https://github.com/owner/repo/pull/9" {
		t.Fatalf("active = %+v", snap.Active)
	}
}

func TestSnapshotWithoutOptionalStoresStillHasNonNullCollections(t *testing.T) {
	store := &snapshotStubStore{}
	snap, err := (SnapshotService{Store: store}).Snapshot(context.Background())
	if err != nil {
		t.Fatalf(`(SnapshotService{Store: store}).Snapshot(context.Background()): %v`, err)
	}
	if snap.Queue == nil || len(snap.Queue) != 0 {
		t.Fatalf("queue = %+v, want empty array", snap.Queue)
	}
	if snap.Daemon == nil || snap.Daemon.State != "absent" {
		t.Fatalf("daemon = %+v, want absent", snap.Daemon)
	}
	if snap.BaseIncident == nil || snap.BaseIncident.ID != "" || snap.BaseIncident.FailedChecks == nil {
		t.Fatalf("base incident = %+v, want empty object", snap.BaseIncident)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf(`json.Marshal(snap): %v`, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf(`json.Unmarshal(b, &raw): %v`, err)
	}
	for _, key := range []string{"active", "recent", "queue", "daemon", "base_incident"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("snapshot JSON is missing %q: %s", key, b)
		}
	}
	if got := string(raw["queue"]); got != "[]" {
		t.Fatalf("queue JSON = %s, want []", got)
	}
	if got := string(raw["daemon"]); !strings.Contains(got, `"state":"absent"`) {
		t.Fatalf("daemon JSON = %s, want state absent", got)
	}
	if got := string(raw["base_incident"]); !strings.Contains(got, `"id":""`) ||
		!strings.Contains(got, `"failed_checks":[]`) {
		t.Fatalf("base incident JSON = %s, want empty object", got)
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
		t.Fatalf(`json.Marshal(got): %v`, err)
	}
	var decoded struct {
		Checks []struct {
			Name            string `json:"name"`
			State           string `json:"state"`
			DurationSeconds int64  `json:"duration_seconds"`
		} `json:"checks"`
		CIWallSeconds int64 `json:"ci_wall_seconds"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf(`json.Unmarshal(b, &decoded): %v`, err)
	}
	if decoded.Checks == nil || len(decoded.Checks) != 0 {
		t.Fatalf("json checks = %+v, want []", decoded.Checks)
	}
	if decoded.CIWallSeconds != 0 {
		t.Fatalf("json ci_wall_seconds = %d, want 0", decoded.CIWallSeconds)
	}

	run.Checks = []core.CheckResult{{Name: "ci", State: core.CheckFailure}}
	b, err = json.Marshal(makeRunSnapshot(run, nil, nil, now))
	if err != nil {
		t.Fatalf(`json.Marshal(makeRunSnapshot(run, nil, nil, now)): %v`, err)
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf(`json.Unmarshal(b, &decoded): %v`, err)
	}
	if len(decoded.Checks) != 1 || decoded.Checks[0].Name != "ci" ||
		decoded.Checks[0].State != "failure" || decoded.Checks[0].DurationSeconds != 0 {
		t.Fatalf("json checks = %+v", decoded.Checks)
	}
}

func TestFailedCheckNamesKeepsArbitraryUTF8Names(t *testing.T) {
	names := []string{"ci\vbuild", "日本語", "emoji 🚀", "quote \" and \\ slash"}
	raw := sqliteChecks(names)
	got := failedCheckNames(raw)
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("failedCheckNames(%q) = %q, want %q", raw, got, names)
	}
}

func TestLaunchSourceCoversMCPCLIUIAndDaemon(t *testing.T) {
	cases := []struct {
		raw  string
		want core.LaunchSource
	}{
		{"mcp", core.LaunchMCP}, {"opencode", core.LaunchMCP},
		{"cli", core.LaunchCLI}, {"ui", core.LaunchUI}, {"tui", core.LaunchUI},
		{"daemon", core.LaunchDaemon}, {"", core.LaunchDaemon}, {"token-abc", core.LaunchDaemon},
	}
	for _, tc := range cases {
		if got := core.ParseLaunchSource(tc.raw); got != tc.want {
			t.Fatalf("ParseLaunchSource(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	run := core.NewRun("run-launch", "T-LAUNCH", time.Now().UTC())
	run.RecordLaunch(core.LaunchUI, " operator ")
	if run.LaunchSource != core.LaunchUI || run.AgentIdentity != "operator" {
		t.Fatalf("launch = %q, agent = %q", run.LaunchSource, run.AgentIdentity)
	}
	if run.ParentSessionID != "" {
		t.Fatalf("parent session changed: %q", run.ParentSessionID)
	}
}

func TestBlockedMetricMapStaysBoundedWithDistinctReasons(t *testing.T) {
	now := time.Now().UTC()
	reasons := []string{
		"gate failed: provider error at sha 0123456789abcdef",
		"gate failed: provider error at sha abcdef0123456789",
		"gate failed: provider error at sha deadbeefdeadbeef",
		"base_red on main at sha 1111111111111111",
		"base_red on main at sha 2222222222222222",
		"rebase attempts exhausted on 3333333333333333",
		"fix attempts exhausted on 4444444444444444",
		"idle without PR or changes 5555555555555555",
	}
	var active []*core.Run
	histories := map[string][]Decision{}
	for i, reason := range reasons {
		run := core.NewRun(core.RunID(fmt.Sprintf("run-%d", i)), core.TaskID(fmt.Sprintf("T-%d", i)), now)
		run.State = core.RunBlocked
		run.Reason = reason
		active = append(active, run)

		id := fmt.Sprintf("decision-run-%d", i)
		histories[id] = []Decision{{
			Kind:    "reconcile.decision",
			RunID:   id,
			Verdict: string(core.VerdictBlocked),
			Reason:  fmt.Sprintf("gate failed: provider error %d at sha %x", i, i),
		}}
	}

	snapshots := make([]RunSnapshot, 0, len(active))
	for _, run := range active {
		snapshots = append(snapshots, makeRunSnapshot(run, nil, nil, now))
	}
	m := makeMetrics(snapshots, nil, nil, histories, nil)
	if len(m.Blocked) > 6 {
		t.Fatalf("blocked keys = %d, want at most 6: %+v", len(m.Blocked), m.Blocked)
	}
	for key := range m.Blocked {
		if strings.Contains(strings.ToLower(key), "sha") || len(key) > 20 {
			t.Fatalf("blocked key %q carries reason detail", key)
		}
	}
	if got := m.Blocked["gate failed"]; got != 11 {
		t.Fatalf("gate failed count = %d, want 11: %+v", got, m.Blocked)
	}
	sum := 0
	for _, n := range m.Blocked {
		sum += n
	}
	if sum != m.BlockedCount || m.BlockedCount != len(reasons)*2 {
		t.Fatalf("blocked sum = %d, count = %d, want %d", sum, m.BlockedCount, len(reasons)*2)
	}
}

func TestSnapshotSerializesLaunchSourceAndMetrics(t *testing.T) {
	now := time.Now().UTC()
	mcpRun := core.NewRun("run-mcp", "T-MCP", now)
	mcpRun.RecordLaunch(core.LaunchMCP, "orchestrator")
	cliRun := core.NewRun("run-cli", "T-CLI", now)
	cliRun.RecordLaunch(core.LaunchCLI, "")
	store := &snapshotStubStore{active: []*core.Run{mcpRun, cliRun, core.NewRun("run-old", "T-OLD", now)}}
	snap, err := (SnapshotService{Store: store}).Snapshot(context.Background())
	if err != nil {
		t.Fatalf(`(SnapshotService{Store: store}).Snapshot(context.Background()): %v`, err)
	}
	if snap.Active[0].LaunchSource != "mcp" || snap.Active[0].AgentIdentity != "orchestrator" {
		t.Fatalf("mcp snapshot = %+v", snap.Active[0])
	}
	if snap.Active[1].LaunchSource != "cli" || snap.Active[1].AgentIdentity != "" {
		t.Fatalf("cli snapshot = %+v", snap.Active[1])
	}
	want := map[string]int{"mcp": 1, "cli": 1, "daemon": 1}
	if !reflect.DeepEqual(snap.Metrics.LaunchSources, want) {
		t.Fatalf("launch sources = %+v, want %+v", snap.Metrics.LaunchSources, want)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf(`json.Marshal(snap): %v`, err)
	}
	var decoded struct {
		Active []struct {
			LaunchSource  string `json:"launch_source"`
			AgentIdentity string `json:"agent_identity"`
		} `json:"active"`
		Metrics struct {
			LaunchSources map[string]int `json:"launch_sources"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf(`json.Unmarshal(b, &decoded): %v`, err)
	}
	if decoded.Active[0].LaunchSource != "mcp" || decoded.Active[0].AgentIdentity != "orchestrator" {
		t.Fatalf("json launch = %+v", decoded.Active[0])
	}
	if !reflect.DeepEqual(decoded.Metrics.LaunchSources, want) {
		t.Fatalf("json launch sources = %+v, want %+v", decoded.Metrics.LaunchSources, want)
	}
}

type durationStubStore struct {
	active    []*core.Run
	events    []RunEvent
	decisions []Decision
}

func (s *durationStubStore) ListActive(context.Context) ([]*core.Run, error) {
	return s.active, nil
}
func (s *durationStubStore) ListRecent(context.Context, int) ([]*core.Run, error) {
	return nil, nil
}
func (s *durationStubStore) ListRunEvents(context.Context, []string) ([]RunEvent, error) {
	return s.events, nil
}
func (s *durationStubStore) ListDecisions(context.Context, []string, int, int) ([]Decision, error) {
	return s.decisions, nil
}
func (s *durationStubStore) DecisionCounts(context.Context, []string) ([]DecisionCount, error) {
	return nil, nil
}

func TestSnapshotUsesOneDurationPass(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	const runCount = 3
	active := make([]*core.Run, 0, runCount)
	var events []RunEvent
	var want int64
	for i := 0; i < runCount; i++ {
		id := fmt.Sprintf("run-%d", i)
		run := core.NewRun(core.RunID(id), core.TaskID(fmt.Sprintf("T-%d", i)), now.Add(-10*time.Minute))
		run.State = core.RunMerged
		run.UpdatedAt = now.Add(-5 * time.Minute)
		active = append(active, run)
		runEvents := []RunEvent{{RunID: id, State: string(core.RunWaitingCI), At: now.Add(-9 * time.Minute)}}
		events = append(events, runEvents...)
		want += deriveDurations(run, runEvents, now).WaitingCI
	}
	store := &durationStubStore{active: active, events: events}
	snap, err := (SnapshotService{Store: store}).Snapshot(context.Background())
	if err != nil {
		t.Fatalf(`svc.Snapshot(ctx): %v`, err)
	}
	var sum int64
	for _, rs := range snap.Active {
		sum += rs.Durations.WaitingCI
	}
	if sum != want {
		t.Fatalf("snapshot durations sum = %d, want %d", sum, want)
	}
	if snap.Metrics.CIWaitSeconds != want {
		t.Fatalf("CI wait = %d, want %d from one duration pass", snap.Metrics.CIWaitSeconds, want)
	}
}

func TestMetricsConsumeSnapshotDurations(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	run := core.NewRun("run-dur", "T-DUR", now.Add(-10*time.Minute))
	run.State = core.RunWaitingCI
	run.UpdatedAt = now.Add(-5 * time.Minute)
	snap := makeRunSnapshot(run, map[string][]RunEvent{
		string(run.ID): {{State: string(core.RunWaitingCI), At: run.UpdatedAt}},
	}, nil, now)

	// A timeline that would yield 60 seconds if metrics walked it again. The
	// snapshot carries 300 seconds, and metrics must use that value.
	stale := map[string][]RunEvent{
		string(run.ID): {{Kind: "state", State: string(core.RunWaitingCI), At: now.Add(-time.Minute)}},
	}
	m := makeMetrics([]RunSnapshot{snap}, nil, stale, nil, nil)
	if m.CIWaitSeconds != snap.Durations.WaitingCI {
		t.Fatalf("CI wait = %d, want snapshot duration %d", m.CIWaitSeconds, snap.Durations.WaitingCI)
	}
	if m.CIWaitSeconds != 300 {
		t.Fatalf("CI wait = %d, want 300", m.CIWaitSeconds)
	}
}

func BenchmarkSnapshotDurationPass(b *testing.B) {
	now := time.Now().UTC()
	const runCount = 500
	const perRun = 20
	active := make([]*core.Run, 0, runCount)
	events := make([]RunEvent, 0, runCount*perRun)
	decisions := make([]Decision, 0, runCount*perRun)
	for i := 0; i < runCount; i++ {
		id := fmt.Sprintf("run-%04d", i)
		run := core.NewRun(core.RunID(id), core.TaskID(fmt.Sprintf("T-%04d", i)), now.Add(-time.Hour))
		run.State = core.RunWaitingCI
		run.UpdatedAt = now.Add(-30 * time.Minute)
		active = append(active, run)
		for j := 0; j < perRun; j++ {
			events = append(events, RunEvent{
				RunID: id, Kind: "state", State: string(core.RunWaitingCI),
				At: run.CreatedAt.Add(time.Duration(j) * time.Minute),
			})
			decisions = append(decisions, Decision{RunID: id, Kind: "transition.spawn"})
		}
	}
	store := &durationStubStore{active: active, events: events, decisions: decisions}
	svc := SnapshotService{Store: store}
	ctx := context.Background()
	base, err := svc.Snapshot(ctx)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		got, err := svc.Snapshot(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if got.Metrics.CIWaitSeconds != base.Metrics.CIWaitSeconds {
			b.Fatalf("CI wait = %d, want %d", got.Metrics.CIWaitSeconds, base.Metrics.CIWaitSeconds)
		}
		if len(got.Active) != len(base.Active) {
			b.Fatalf("active = %d, want %d", len(got.Active), len(base.Active))
		}
	}
	b.ReportMetric(float64(runCount), "runs/op")
	b.ReportMetric(float64(len(events)), "events/op")
}

// sourceVerbs reads a control vocabulary from a Go source file. Both core and
// the TUI declare their verb list as a literal array of named string constants,
// so a source comparison catches drift even though ui/tui is a separate module.
func sourceVerbs(t *testing.T, path, arrayName string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parser.ParseFile(%q): %v", path, err)
	}
	constants := map[string]string{}
	var verbs []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			switch gen.Tok {
			case token.CONST:
				for i, name := range values.Names {
					if i >= len(values.Values) {
						continue
					}
					lit, ok := values.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: unquote %s: %v", path, lit.Value, err)
					}
					constants[name.Name] = value
				}
			case token.VAR:
				if len(values.Names) != 1 || values.Names[0].Name != arrayName {
					continue
				}
				if len(values.Values) != 1 {
					t.Fatalf("%s: %s has %d values", path, arrayName, len(values.Values))
				}
				array, ok := values.Values[0].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s: %s is not a literal array", path, arrayName)
				}
				for _, element := range array.Elts {
					id, ok := element.(*ast.Ident)
					if !ok {
						t.Fatalf("%s: %s has a non-constant element", path, arrayName)
					}
					verb, ok := constants[id.Name]
					if !ok {
						t.Fatalf("%s: verb %s has no constant value", path, id.Name)
					}
					verbs = append(verbs, verb)
				}
			}
		}
	}
	if len(verbs) == 0 {
		t.Fatalf("%s: %s is empty", path, arrayName)
	}
	return verbs
}

func TestControlVocabularySharedBetweenCoreAndTUI(t *testing.T) {
	// The TUI cannot import pkg/core, so it mirrors the vocabulary in
	// ui/tui/api.go. Compare the two sources and the runtime accessor. A new
	// core action without a TUI action must fail this test.
	coreVerbs := sourceVerbs(t, "../../pkg/core/control.go", "ControlActions")
	tuiVerbs := sourceVerbs(t, "../../ui/tui/api.go", "actions")
	if !reflect.DeepEqual(tuiVerbs, coreVerbs) {
		t.Fatalf("TUI actions = %v, core.ControlActions = %v", tuiVerbs, coreVerbs)
	}
	if !reflect.DeepEqual(coreVerbs, core.ControlActionNames()) {
		t.Fatalf("source verbs = %v, core.ControlActionNames = %v", coreVerbs, core.ControlActionNames())
	}
}

func TestAddCountMetricsKeepsEveryCounter(t *testing.T) {
	counts := []DecisionCount{
		{Kind: "dispatch.candidates", Count: 2},
		{Kind: "dispatch.skip", Verdict: "overlap", Count: 3},
		{Kind: "dispatch.skip", Verdict: "blocked_by", Count: 4},
		{Kind: "transition.spawn", Count: 5},
		{Kind: "transition.rebase", Action: "rebased", Count: 6},
		{Kind: "transition.rebase", Action: "stale", Count: 7},
		{Kind: "transition.wake", Action: "woke", Count: 8},
		{Kind: "transition.wake", Action: "failed", Count: 9},
		{Kind: "unknown.kind", Count: 10},
	}
	m := Metrics{Skips: map[string]int{}}
	addCountMetrics(&m, counts)
	if m.DispatchCycles != 2 {
		t.Fatalf("dispatch cycles = %d, want 2", m.DispatchCycles)
	}
	if m.SkipCount != 7 {
		t.Fatalf("skip count = %d, want 7", m.SkipCount)
	}
	if want := map[string]int{"overlap": 3, "blocked_by": 4}; !reflect.DeepEqual(m.Skips, want) {
		t.Fatalf("skips = %+v, want %+v", m.Skips, want)
	}
	if m.DispatchCount != 5 {
		t.Fatalf("dispatch count = %d, want 5", m.DispatchCount)
	}
	if m.RebaseCount != 6 {
		t.Fatalf("rebase count = %d, want 6", m.RebaseCount)
	}
	if m.FixCount != 8 {
		t.Fatalf("fix count = %d, want 8", m.FixCount)
	}
}
