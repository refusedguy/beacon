package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type StatusService struct{ Store RunStore }

// DefaultRecentLimit is the number of finished runs returned when no limit is set.
const DefaultRecentLimit = 20

// DefaultDecisionsLimit caps the decisions attached to one run in a snapshot.
const DefaultDecisionsLimit = 20

// DefaultOrphanDecisionsLimit caps run-less decisions, which otherwise grow
// without bound as dispatch cycles accumulate.
const DefaultOrphanDecisionsLimit = 100

// DecisionCount is a compact aggregate returned by the store for a snapshot.
type DecisionCount struct {
	Kind    string
	Verdict string
	Action  string
	Count   int
}

func (s StatusService) Active(ctx context.Context) ([]*core.Run, error) {
	return s.Store.ListActive(ctx)
}

func (s StatusService) Recent(ctx context.Context, limit int) ([]*core.Run, error) {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	return s.Store.ListRecent(ctx, limit)
}

// TickReconciler is the part of the reconciler a forced tick needs.
type TickReconciler interface {
	Reconcile(ctx context.Context, pr int) (core.MergeDecision, error)
}

// ReconcileDecision is the policy verdict produced by one forced tick.
type ReconcileDecision struct {
	Verdict   string `json:"verdict"`
	Reason    string `json:"reason"`
	Criterion string `json:"criterion"`
}

// ReconcileResult is the structured outcome of one forced reconciler tick.
type ReconcileResult struct {
	PR       int               `json:"pr"`
	Decision ReconcileDecision `json:"decision"`
	State    string            `json:"state"`
	Error    string            `json:"error,omitempty"`
}

// ReconcileService runs one reconciler tick on demand. It captures errors and
// panics in the result so a forced tick can never stop the daemon loop.
type ReconcileService struct {
	Reconciler TickReconciler
	Store      RunStore
}

// Tick forces exactly one reconciler pass for pr and reports what happened. A
// panic is recovered into Error, matching the reconciler loop's own guard.
func (s *ReconcileService) Tick(ctx context.Context, pr int) (out ReconcileResult) {
	out.PR = pr
	defer func() {
		if rec := recover(); rec != nil {
			out.Error = fmt.Sprintf("reconcile panic: %v", rec)
		}
	}()
	if pr <= 0 {
		out.Error = "pr must be positive"
		return out
	}
	if s.Reconciler == nil {
		out.Error = "reconciler is not configured"
		return out
	}
	decision, err := s.Reconciler.Reconcile(ctx, pr)
	out.Decision = ReconcileDecision{
		Verdict:   string(decision.Verdict),
		Reason:    decision.Reason,
		Criterion: decision.Criterion,
	}
	if err != nil {
		out.Error = err.Error()
	}
	if s.Store != nil {
		if run, lookupErr := s.Store.FindByPR(ctx, pr); lookupErr == nil && run != nil {
			out.State = string(run.State)
		}
	}
	return out
}

type WaitService struct {
	Store    RunStore
	Interval time.Duration
}

// WaitFor blocks until the run is terminal or the context deadline expires.
func (w WaitService) WaitFor(ctx context.Context, id core.RunID) (*core.Run, error) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		run, err := w.Store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, core.Errorf(core.CodeNotFound, "run %s", id)
		}
		if run.State.Terminal() || run.State == core.RunWaitingCI {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunEvent is one timestamped entry in a run's timeline.
type RunEvent struct {
	ID       int64     `json:"id"`
	RunID    string    `json:"run_id"`
	Kind     string    `json:"kind"`
	State    string    `json:"state"`
	At       time.Time `json:"at"`
	HeadSHA  string    `json:"head_sha"`
	PRNumber int       `json:"pr"`
	Reason   string    `json:"reason"`
}

// Durations are seconds spent in a state, derived from timeline entries.
type Durations struct {
	WaitingCI int64 `json:"waiting_ci_seconds"`
	Running   int64 `json:"running_seconds"`
	Blocked   int64 `json:"blocked_seconds"`
}

// CheckSnapshot is the UI-facing view of one CI check.
type CheckSnapshot struct {
	Name            string `json:"name"`
	State           string `json:"state"`
	DurationSeconds int64  `json:"duration_seconds"`
}

// RunSnapshot carries a run, its history and its derived timings.
type RunSnapshot struct {
	ID                string          `json:"id"`
	TaskID            string          `json:"task_id"`
	State             string          `json:"state"`
	PR                int             `json:"pr"`
	PRURL             string          `json:"pr_url,omitempty"`
	HeadSHA           string          `json:"head_sha"`
	Branch            string          `json:"branch"`
	SessionID         string          `json:"session_id,omitempty"`
	IsFixer           bool            `json:"is_fixer,omitempty"`
	BlockedIncidentID string          `json:"blocked_incident_id,omitempty"`
	Reason            string          `json:"reason"`
	RebaseAttempts    int             `json:"rebase_attempts"`
	FixAttempts       int             `json:"fix_attempts"`
	MaxAttempts       int             `json:"max_attempts"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Durations         Durations       `json:"durations"`
	Timeline          []RunEvent      `json:"timeline"`
	Decisions         []Decision      `json:"decisions"`
	Checks            []CheckSnapshot `json:"checks"`
}

// Metrics are numeric totals for the whole snapshot.
type Metrics struct {
	DispatchCount  int            `json:"dispatch_count"`
	DispatchCycles int            `json:"dispatch_cycles"`
	SkipCount      int            `json:"skip_count"`
	Skips          map[string]int `json:"skips"`
	RebaseCount    int            `json:"rebase_count"`
	FixCount       int            `json:"fix_count"`
	MergeCount     int            `json:"merge_count"`
	BlockedCount   int            `json:"blocked_count"`
	Blocked        map[string]int `json:"blocked"`
	CIWaitSeconds  int64          `json:"ci_wait_seconds"`
	ActiveCount    int            `json:"active_count"`
	RecentCount    int            `json:"recent_count"`
}

// QueueItemSnapshot is one local task card in the operator's queue.
type QueueItemSnapshot struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Priority  int      `json:"priority"`
	Status    string   `json:"status"`
	DependsOn []string `json:"depends_on"`
}

// QueueTask is a local task card with its unresolved dependencies.
type QueueTask struct {
	ID        string
	Title     string
	Priority  int
	DependsOn []string
	Waiting   bool
}

// BaseIncidentSnapshot describes the open base-branch incident being fixed.
type BaseIncidentSnapshot struct {
	ID           string   `json:"id"`
	BaseBranch   string   `json:"base_branch"`
	FailedChecks []string `json:"failed_checks"`
	FixerRunID   string   `json:"fixer_run_id"`
	Attempts     int      `json:"attempts"`
}

// DaemonSnapshot is the daemon liveness view handed in by the composition
// layer. internal/app never reads daemon state files itself.
type DaemonSnapshot struct {
	State               string  `json:"state"`
	PID                 int     `json:"pid,omitempty"`
	HeartbeatAgeSeconds float64 `json:"heartbeat_age_seconds"`
	Error               string  `json:"error,omitempty"`
}

// Snapshot is the single response the status panel needs. The active and
// recent sections keep the field names of the older two-call API.
type Snapshot struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Active       []RunSnapshot         `json:"active"`
	Recent       []RunSnapshot         `json:"recent"`
	Queue        []QueueItemSnapshot   `json:"queue"`
	BaseIncident *BaseIncidentSnapshot `json:"base_incident"`
	Daemon       *DaemonSnapshot       `json:"daemon"`
	Metrics      Metrics               `json:"metrics"`
}

// QueueStore is an optional read side for the local task queue. A store that
// omits it yields an empty, non-null queue.
type QueueStore interface {
	ListQueueTasks(ctx context.Context) ([]QueueTask, error)
}

// IncidentStore is an optional read side for the current base incident.
type IncidentStore interface {
	CurrentIncident(ctx context.Context) (*core.BaseIncident, error)
}

// SnapshotStore is the batched read side of a snapshot. History queries take
// all run IDs at once so a growing run count adds no calls.
type SnapshotStore interface {
	ListActive(ctx context.Context) ([]*core.Run, error)
	ListRecent(ctx context.Context, limit int) ([]*core.Run, error)
	ListRunEvents(ctx context.Context, runIDs []string) ([]RunEvent, error)
	ListDecisions(ctx context.Context, runIDs []string, limit, orphanLimit int) ([]Decision, error)
	DecisionCounts(ctx context.Context, runIDs []string) ([]DecisionCount, error)
}

type SnapshotService struct {
	Store                SnapshotStore
	RecentLimit          int
	DecisionsLimit       int
	OrphanDecisionsLimit int
	Repo                 string
	Daemon               *DaemonSnapshot
}

// Snapshot returns active and recent runs with their timelines, decisions and
// aggregate metrics in one pass over the store.
func (s SnapshotService) Snapshot(ctx context.Context) (*Snapshot, error) {
	active, err := s.Store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	limit := s.RecentLimit
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	recent, err := s.Store.ListRecent(ctx, limit)
	if err != nil {
		return nil, err
	}
	ids := collectRunIDs(active, recent)
	events, err := s.Store.ListRunEvents(ctx, ids)
	if err != nil {
		return nil, err
	}
	decisionLimit, orphanLimit := snapshotDecisionLimits(s)
	decisions, err := s.Store.ListDecisions(ctx, ids, decisionLimit, orphanLimit)
	if err != nil {
		return nil, err
	}
	counts, err := s.Store.DecisionCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	queue, err := readQueue(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	incident, err := readCurrentIncident(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	timeline, histories := groupHistory(events, decisions)
	now := time.Now().UTC()
	daemon := s.Daemon
	if daemon == nil {
		daemon = &DaemonSnapshot{State: "absent"}
	}
	out := &Snapshot{
		GeneratedAt:  now,
		Active:       make([]RunSnapshot, 0, len(active)),
		Recent:       make([]RunSnapshot, 0, len(recent)),
		Queue:        makeQueue(queue),
		BaseIncident: makeBaseIncident(incident),
		Daemon:       daemon,
		Metrics:      makeMetrics(active, recent, timeline, histories, counts, now),
	}
	for _, run := range active {
		out.Active = append(out.Active, makeRunSnapshot(run, timeline, histories, now, s.Repo))
	}
	for _, run := range recent {
		out.Recent = append(out.Recent, makeRunSnapshot(run, timeline, histories, now, s.Repo))
	}
	return out, nil
}

func snapshotDecisionLimits(s SnapshotService) (int, int) {
	decisionLimit := s.DecisionsLimit
	if decisionLimit <= 0 {
		decisionLimit = DefaultDecisionsLimit
	}
	orphanLimit := s.OrphanDecisionsLimit
	if orphanLimit <= 0 {
		orphanLimit = DefaultOrphanDecisionsLimit
	}
	return decisionLimit, orphanLimit
}

func readQueue(ctx context.Context, store SnapshotStore) ([]QueueTask, error) {
	source, ok := store.(QueueStore)
	if !ok {
		return nil, nil
	}
	return source.ListQueueTasks(ctx)
}

func readCurrentIncident(ctx context.Context, store SnapshotStore) (*core.BaseIncident, error) {
	source, ok := store.(IncidentStore)
	if !ok {
		return nil, nil
	}
	return source.CurrentIncident(ctx)
}

func makeQueue(tasks []QueueTask) []QueueItemSnapshot {
	out := make([]QueueItemSnapshot, 0, len(tasks))
	for _, task := range tasks {
		status := "ready"
		if task.Waiting {
			status = "waiting_deps"
		}
		out = append(out, QueueItemSnapshot{
			ID: task.ID, Title: task.Title, Priority: task.Priority,
			Status: status, DependsOn: nonNullStrings(task.DependsOn),
		})
	}
	return out
}

func makeBaseIncident(in *core.BaseIncident) *BaseIncidentSnapshot {
	if in == nil {
		return &BaseIncidentSnapshot{ID: "", FailedChecks: []string{}}
	}
	return &BaseIncidentSnapshot{
		ID: in.ID, BaseBranch: in.BaseBranch,
		FailedChecks: failedCheckNames(in.FailedChecksJSON),
		FixerRunID:   string(in.FixerRunID), Attempts: in.Attempts,
	}
}

func failedCheckNames(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	var named []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &named); err == nil {
		for _, c := range named {
			if c.Name != "" {
				out = append(out, c.Name)
			}
		}
		return out
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err == nil {
		for _, name := range names {
			if name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func nonNullStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func collectRunIDs(groups ...[]*core.Run) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, group := range groups {
		for _, run := range group {
			id := string(run.ID)
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func groupHistory(events []RunEvent, decisions []Decision) (map[string][]RunEvent, map[string][]Decision) {
	timeline := make(map[string][]RunEvent)
	histories := make(map[string][]Decision)
	for _, event := range events {
		timeline[event.RunID] = append(timeline[event.RunID], event)
	}
	for _, decision := range decisions {
		histories[decision.RunID] = append(histories[decision.RunID], decision)
	}
	return timeline, histories
}

func makeRunSnapshot(run *core.Run, timeline map[string][]RunEvent, histories map[string][]Decision, now time.Time, repo ...string) RunSnapshot {
	events := timeline[string(run.ID)]
	if events == nil {
		events = []RunEvent{}
	}
	decisions := histories[string(run.ID)]
	if decisions == nil {
		decisions = []Decision{}
	}
	link := ""
	if len(repo) > 0 {
		link = prLink(repo[0], run.PRNumber)
	}
	return RunSnapshot{
		ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
		PR: run.PRNumber, PRURL: link, HeadSHA: run.HeadSHA, Branch: run.Branch,
		SessionID: run.SessionID, IsFixer: run.IsFixer,
		BlockedIncidentID: run.BlockedIncidentID, Reason: run.Reason,
		RebaseAttempts: run.RebaseAttempts, FixAttempts: run.FixAttempts,
		MaxAttempts: run.MaxAttempts, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		Durations: deriveDurations(run, events, now), Timeline: events, Decisions: decisions,
		Checks: checkSnapshots(run.Checks),
	}
}

// prLink builds the GitHub PR URL. A repo that is not owner/repo has no URL,
// which keeps other SCM providers from showing a broken link.
func prLink(repo string, pr int) string {
	if pr <= 0 {
		return ""
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return "https://github.com/" + owner + "/" + name + "/pull/" + fmt.Sprint(pr)
}

// checkSnapshots maps stored checks to the UI contract without any SCM call.
// A run with no observed checks returns an empty array, never null.
func checkSnapshots(checks []core.CheckResult) []CheckSnapshot {
	out := make([]CheckSnapshot, 0, len(checks))
	for _, c := range checks {
		out = append(out, CheckSnapshot{
			Name: c.Name, State: string(c.State), DurationSeconds: c.DurationSeconds(),
		})
	}
	return out
}

// deriveDurations measures each state interval. A live run has no closing
// event, so its last interval runs until now and the panel sees a ticking
// timer. Terminal runs keep the fixed UpdatedAt boundary.
func deriveDurations(run *core.Run, events []RunEvent, now time.Time) Durations {
	var out Durations
	endOfRun := run.UpdatedAt
	if !run.State.Terminal() && now.After(endOfRun) {
		endOfRun = now
	}
	for i, event := range events {
		end := endOfRun
		if i+1 < len(events) {
			end = events[i+1].At
		}
		if end.After(event.At) {
			addDuration(&out, core.RunState(event.State), int64(end.Sub(event.At).Seconds()))
		}
	}
	// Older state databases have no events. Use the run's own window so the
	// panel still shows an elapsed time.
	if len(events) == 0 && endOfRun.After(run.CreatedAt) {
		addDuration(&out, run.State, int64(endOfRun.Sub(run.CreatedAt).Seconds()))
	}
	return out
}

func addDuration(out *Durations, state core.RunState, seconds int64) {
	switch state {
	case core.RunWaitingCI:
		out.WaitingCI += seconds
	case core.RunRunning:
		out.Running += seconds
	case core.RunBlocked:
		out.Blocked += seconds
	}
}

func makeMetrics(
	active, recent []*core.Run,
	timeline map[string][]RunEvent,
	histories map[string][]Decision,
	counts []DecisionCount,
	now time.Time,
) Metrics {
	m := Metrics{Skips: map[string]int{}, Blocked: map[string]int{}}
	merged := make(map[string]bool)
	blocked := make(map[string]bool)
	addRunMetrics(&m, active, recent, timeline, merged, blocked, now)
	for _, events := range timeline {
		for _, event := range events {
			if event.Kind == "merged" {
				merged[event.RunID] = true
			}
		}
	}
	addDecisionMetrics(&m, histories, merged, blocked)
	addCountMetrics(&m, counts)
	m.MergeCount = len(merged)
	return m
}

func addCountMetrics(m *Metrics, counts []DecisionCount) {
	for _, c := range counts {
		switch c.Kind {
		case "dispatch.candidates":
			m.DispatchCycles += c.Count
		case "dispatch.skip":
			m.SkipCount += c.Count
			m.Skips[c.Verdict] += c.Count
		case "transition.spawn":
			m.DispatchCount += c.Count
		case "transition.rebase":
			if c.Action == "rebased" {
				m.RebaseCount += c.Count
			}
		case "transition.wake":
			if c.Action == "woke" {
				m.FixCount += c.Count
			}
		}
	}
}

func addRunMetrics(m *Metrics, active, recent []*core.Run, timeline map[string][]RunEvent, merged, blocked map[string]bool, now time.Time) {
	for _, group := range [][]*core.Run{active, recent} {
		for _, run := range group {
			m.CIWaitSeconds += deriveDurations(run, timeline[string(run.ID)], now).WaitingCI
			id := string(run.ID)
			if run.State == core.RunMerged {
				merged[id] = true
			}
			if run.State == core.RunBlocked && !blocked[id] {
				blocked[id] = true
				m.Blocked[run.Reason]++
				m.BlockedCount++
			}
		}
	}
	m.ActiveCount = len(active)
	m.RecentCount = len(recent)
}

func addDecisionMetrics(m *Metrics, histories map[string][]Decision, merged, blocked map[string]bool) {
	for _, decisions := range histories {
		for _, decision := range decisions {
			switch decision.Kind {
			case "transition.merge":
				merged[decision.RunID] = true
			case "reconcile.decision":
				if decision.Verdict == string(core.VerdictBlocked) && !blocked[decision.RunID] {
					blocked[decision.RunID] = true
					m.Blocked[decision.Reason]++
					m.BlockedCount++
				}
			}
		}
	}
}
