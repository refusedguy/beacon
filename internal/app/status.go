package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// Shared zero-length slices keep empty JSON collections non-null without an
// allocation per snapshot field or queue item.
var (
	emptyRunEvents  = []RunEvent{}
	emptyDecisions  = []Decision{}
	emptyStringList = []string{}
)

// StatusStore is the run read side StatusService needs.
type StatusStore interface {
	ListActive(ctx context.Context) ([]*core.Run, error)
	ListRecent(ctx context.Context, limit int) ([]*core.Run, error)
}

// StatusService lists active and recently finished runs from a store.
type StatusService struct{ Store StatusStore }

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

// Active returns the runs that have not reached a terminal state.
func (s StatusService) Active(ctx context.Context) ([]*core.Run, error) {
	return s.Store.ListActive(ctx)
}

// Recent returns finished runs, using DefaultRecentLimit when limit is not positive.
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

// ReconcileStore is the run lookup ReconcileService needs for the tick's state.
type ReconcileStore interface {
	FindByPR(ctx context.Context, pr int) (*core.Run, error)
}

// ReconcileService runs one reconciler tick on demand. It captures errors and
// panics in the result so a forced tick can never stop the daemon loop.
type ReconcileService struct {
	Reconciler TickReconciler
	Store      ReconcileStore
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

// WaitStore is the run lookup WaitService needs while it polls.
type WaitStore interface {
	Get(ctx context.Context, id core.RunID) (*core.Run, error)
}

// WaitService polls one run until it reaches a verdict or a terminal state.
type WaitService struct {
	Store    WaitStore
	Interval time.Duration
}

// WaitFor blocks until the run is terminal or the context deadline expires.
func (w WaitService) WaitFor(ctx context.Context, id core.RunID) (*core.Run, error) {
	if w.Store == nil {
		return nil, core.Errorf(core.CodeUnavailable, "wait store is not configured")
	}
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

// activeTaskStates answers which of ids have an active run in one store call.
// The fallback keeps narrow store fakes and adapters without the projection
// working; the rule stays the same because it delegates to FindActiveByTask.
func activeTaskStates(ctx context.Context, store any, ids []string) (map[string]core.RunState, error) {
	active := make(map[string]core.RunState)
	if store == nil || len(ids) == 0 {
		return active, nil
	}
	if finder, ok := store.(ActiveTaskFinder); ok {
		return finder.ActiveTaskIDs(ctx, ids)
	}
	if finder, ok := store.(ActiveRunFinder); ok {
		for _, id := range ids {
			run, err := finder.FindActiveByTask(ctx, core.TaskID(id))
			if err != nil {
				return nil, err
			}
			// A terminal run is authoritative: its PR was merged, closed or
			// cancelled. It must not act as a phantom in-flight blocker.
			if run != nil && !run.State.Terminal() {
				active[id] = run.State
			}
		}
	}
	return active, nil
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

// SnapshotService builds the operator panel's response from batched store reads.
type SnapshotService struct {
	Store                SnapshotStore
	RecentLimit          int
	DecisionsLimit       int
	OrphanDecisionsLimit int
	Repo                 string
	Daemon               *DaemonSnapshot
	// Now supplies the snapshot generation time. A nil Now reads the wall
	// clock, which keeps production behavior unchanged. Benchmarks inject a
	// fixed clock so durations and encoded bytes stay identical across passes.
	Now func() time.Time
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
	now := snapshotNow(s)
	daemon := s.Daemon
	if daemon == nil {
		daemon = &DaemonSnapshot{State: "absent"}
	}
	prBaseURL := prBase(s.Repo)
	activeSnapshots := make([]RunSnapshot, 0, len(active))
	for _, run := range active {
		activeSnapshots = append(activeSnapshots, makeRunSnapshot(run, timeline, histories, now, prBaseURL))
	}
	recentSnapshots := make([]RunSnapshot, 0, len(recent))
	for _, run := range recent {
		recentSnapshots = append(recentSnapshots, makeRunSnapshot(run, timeline, histories, now, prBaseURL))
	}
	out := &Snapshot{
		GeneratedAt:  now,
		Active:       activeSnapshots,
		Recent:       recentSnapshots,
		Queue:        makeQueue(queue),
		BaseIncident: makeBaseIncident(incident),
		Daemon:       daemon,
		Metrics:      makeMetrics(activeSnapshots, recentSnapshots, timeline, histories, counts),
	}
	return out, nil
}

// snapshotNow applies the injected clock when one is set. Keeping the choice
// out of Snapshot holds that method at its prior statement count.
func snapshotNow(s SnapshotService) time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
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
		return emptyStringList
	}
	return values
}

// collectRunIDs returns the distinct non-empty run IDs in group order. The
// first pass counts them into seen, so the result is allocated once at its
// final size instead of growing through repeated appends. The second pass
// clears each seen entry as it is emitted, keeping one map and stable order.
func collectRunIDs(groups ...[]*core.Run) []string {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	seen := make(map[string]bool, total)
	for _, group := range groups {
		for _, run := range group {
			id := string(run.ID)
			if id != "" {
				seen[id] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for _, group := range groups {
		for _, run := range group {
			id := string(run.ID)
			if id != "" && seen[id] {
				seen[id] = false
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// groupHistory buckets events and decisions by run. A counting pass sizes each
// bucket before it is filled, so a run with many rows allocates its backing
// array once instead of growing one append at a time.
func groupHistory(events []RunEvent, decisions []Decision) (map[string][]RunEvent, map[string][]Decision) {
	eventCounts := make(map[string]int, len(events))
	for _, event := range events {
		eventCounts[event.RunID]++
	}
	decisionCounts := make(map[string]int, len(decisions))
	for _, decision := range decisions {
		decisionCounts[decision.RunID]++
	}
	timeline := make(map[string][]RunEvent, len(eventCounts))
	for id, n := range eventCounts {
		timeline[id] = make([]RunEvent, 0, n)
	}
	for _, event := range events {
		timeline[event.RunID] = append(timeline[event.RunID], event)
	}
	histories := make(map[string][]Decision, len(decisionCounts))
	for id, n := range decisionCounts {
		histories[id] = make([]Decision, 0, n)
	}
	for _, decision := range decisions {
		histories[decision.RunID] = append(histories[decision.RunID], decision)
	}
	return timeline, histories
}

// makeRunSnapshot builds one run's wire view. prBaseURL is the PR URL prefix
// computed once per snapshot, so a run only appends its number to it.
func makeRunSnapshot(run *core.Run, timeline map[string][]RunEvent, histories map[string][]Decision, now time.Time, prBaseURL string) RunSnapshot {
	events := timeline[string(run.ID)]
	if events == nil {
		events = emptyRunEvents
	}
	decisions := histories[string(run.ID)]
	if decisions == nil {
		decisions = emptyDecisions
	}
	link := ""
	if prBaseURL != "" && run.PRNumber > 0 {
		link = prBaseURL + strconv.Itoa(run.PRNumber)
	}
	return RunSnapshot{
		ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
		PR: run.PRNumber, PRURL: link, HeadSHA: run.HeadSHA, Branch: run.Branch,
		SessionID: run.SessionID, IsFixer: run.IsFixer,
		LaunchSource: string(run.LaunchSource), AgentIdentity: run.AgentIdentity,
		BlockedIncidentID: run.BlockedIncidentID, Reason: run.Reason,
		RebaseAttempts: run.RebaseAttempts, FixAttempts: run.FixAttempts,
		MaxAttempts: run.MaxAttempts, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		Durations: deriveDurations(run, events, now), Timeline: events, Decisions: decisions,
		Checks: checkSnapshots(run.Checks), CIWallSeconds: core.CIWallSeconds(run.Checks),
		CICriticalPath: criticalPathSeconds(run.Checks),
	}
}

// prBase builds the GitHub PR URL prefix. A repo that is not owner/repo has no
// prefix, which keeps other SCM providers from showing a broken link. Snapshot
// calls this once and reuses the prefix for every run.
func prBase(repo string) string {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return "https://github.com/" + owner + "/" + name + "/pull/"
}

// prLink builds the full PR URL from a repo string. Callers that already hold a
// prefix from prBase append strconv.Itoa(pr) directly instead.
func prLink(repo string, pr int) string {
	base := prBase(repo)
	if base == "" || pr <= 0 {
		return ""
	}
	return base + strconv.Itoa(pr)
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
