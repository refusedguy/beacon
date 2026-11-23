package app

import (
	"context"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type StatusService struct{ Store RunStore }

// DefaultRecentLimit is the number of finished runs returned when no limit is set.
const DefaultRecentLimit = 20

func (s StatusService) Active(ctx context.Context) ([]*core.Run, error) {
	return s.Store.ListActive(ctx)
}

func (s StatusService) Recent(ctx context.Context, limit int) ([]*core.Run, error) {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	return s.Store.ListRecent(ctx, limit)
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

// RunSnapshot carries a run, its history and its derived timings.
type RunSnapshot struct {
	ID             string     `json:"id"`
	TaskID         string     `json:"task_id"`
	State          string     `json:"state"`
	PR             int        `json:"pr"`
	HeadSHA        string     `json:"head_sha"`
	Branch         string     `json:"branch"`
	Reason         string     `json:"reason"`
	RebaseAttempts int        `json:"rebase_attempts"`
	FixAttempts    int        `json:"fix_attempts"`
	MaxAttempts    int        `json:"max_attempts"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Durations      Durations  `json:"durations"`
	Timeline       []RunEvent `json:"timeline"`
	Decisions      []Decision `json:"decisions"`
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

// Snapshot is the single response the status panel needs. The active and
// recent sections keep the field names of the older two-call API.
type Snapshot struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Active      []RunSnapshot `json:"active"`
	Recent      []RunSnapshot `json:"recent"`
	Metrics     Metrics       `json:"metrics"`
}

// SnapshotStore is the batched read side of a snapshot. History queries take
// all run IDs at once so a growing run count adds no calls.
type SnapshotStore interface {
	ListActive(ctx context.Context) ([]*core.Run, error)
	ListRecent(ctx context.Context, limit int) ([]*core.Run, error)
	ListRunEvents(ctx context.Context, runIDs []string) ([]RunEvent, error)
	ListDecisions(ctx context.Context, runIDs []string) ([]Decision, error)
}

type SnapshotService struct {
	Store       SnapshotStore
	RecentLimit int
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
	decisions, err := s.Store.ListDecisions(ctx, ids)
	if err != nil {
		return nil, err
	}
	timeline, histories := groupHistory(events, decisions)
	out := &Snapshot{
		GeneratedAt: time.Now().UTC(),
		Active:      make([]RunSnapshot, 0, len(active)),
		Recent:      make([]RunSnapshot, 0, len(recent)),
		Metrics:     makeMetrics(active, recent, timeline, histories),
	}
	for _, run := range active {
		out.Active = append(out.Active, makeRunSnapshot(run, timeline, histories))
	}
	for _, run := range recent {
		out.Recent = append(out.Recent, makeRunSnapshot(run, timeline, histories))
	}
	return out, nil
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

func makeRunSnapshot(run *core.Run, timeline map[string][]RunEvent, histories map[string][]Decision) RunSnapshot {
	events := timeline[string(run.ID)]
	if events == nil {
		events = []RunEvent{}
	}
	decisions := histories[string(run.ID)]
	if decisions == nil {
		decisions = []Decision{}
	}
	return RunSnapshot{
		ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
		PR: run.PRNumber, HeadSHA: run.HeadSHA, Branch: run.Branch, Reason: run.Reason,
		RebaseAttempts: run.RebaseAttempts, FixAttempts: run.FixAttempts,
		MaxAttempts: run.MaxAttempts, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		Durations: deriveDurations(run, events), Timeline: events, Decisions: decisions,
	}
}

func deriveDurations(run *core.Run, events []RunEvent) Durations {
	var out Durations
	for i, event := range events {
		end := run.UpdatedAt
		if i+1 < len(events) {
			end = events[i+1].At
		}
		if end.After(event.At) {
			addDuration(&out, core.RunState(event.State), int64(end.Sub(event.At).Seconds()))
		}
	}
	// Older state databases have no events. Use the run's own window for its
	// current state so the panel still shows an elapsed time.
	if len(events) == 0 && run.UpdatedAt.After(run.CreatedAt) {
		addDuration(&out, run.State, int64(run.UpdatedAt.Sub(run.CreatedAt).Seconds()))
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

func makeMetrics(active, recent []*core.Run, timeline map[string][]RunEvent, histories map[string][]Decision) Metrics {
	m := Metrics{Skips: map[string]int{}, Blocked: map[string]int{}}
	merged := make(map[string]bool)
	blocked := make(map[string]bool)
	addRunMetrics(&m, active, recent, timeline, merged, blocked)
	for _, events := range timeline {
		for _, event := range events {
			if event.Kind == "merged" {
				merged[event.RunID] = true
			}
		}
	}
	addDecisionMetrics(&m, histories, merged, blocked)
	m.MergeCount = len(merged)
	return m
}

func addRunMetrics(m *Metrics, active, recent []*core.Run, timeline map[string][]RunEvent, merged, blocked map[string]bool) {
	for _, group := range [][]*core.Run{active, recent} {
		for _, run := range group {
			m.CIWaitSeconds += deriveDurations(run, timeline[string(run.ID)]).WaitingCI
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
			case "dispatch.candidates":
				m.DispatchCycles++
			case "dispatch.skip":
				m.SkipCount++
				m.Skips[decision.Verdict]++
			case "transition.spawn":
				m.DispatchCount++
			case "transition.rebase":
				if decision.Action == "rebased" {
					m.RebaseCount++
				}
			case "transition.wake":
				if decision.Action == "woke" {
					m.FixCount++
				}
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
