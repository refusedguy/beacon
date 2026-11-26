package app

import (
	"context"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

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
// WaitingCI is the total; the three cycle fields split that total by the
// rebase and fix transitions seen so far in the timeline.
type Durations struct {
	WaitingCI        int64 `json:"waiting_ci_seconds"`
	WaitingCIInitial int64 `json:"waiting_ci_initial_seconds"`
	WaitingCIRebase  int64 `json:"waiting_ci_rebase_seconds"`
	WaitingCIFix     int64 `json:"waiting_ci_fix_seconds"`
	Running          int64 `json:"running_seconds"`
	Blocked          int64 `json:"blocked_seconds"`
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
	LaunchSource      string          `json:"launch_source,omitempty"`
	AgentIdentity     string          `json:"agent_identity,omitempty"`
	IsFixer           bool            `json:"is_fixer,omitempty"`
	BlockedIncidentID string          `json:"blocked_incident_id,omitempty"`
	Reason            string          `json:"reason"`
	RebaseAttempts    int             `json:"rebase_attempts"`
	FixAttempts       int             `json:"fix_attempts"`
	MaxAttempts       int             `json:"max_attempts"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Durations         Durations       `json:"durations"`
	CIWallSeconds     int64           `json:"ci_wall_seconds"`
	CICriticalPath    int64           `json:"ci_critical_path_seconds"`
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
	LaunchSources  map[string]int `json:"launch_sources"`
	RebaseCount    int            `json:"rebase_count"`
	FixCount       int            `json:"fix_count"`
	MergeCount     int            `json:"merge_count"`
	BlockedCount   int            `json:"blocked_count"`
	Blocked        map[string]int `json:"blocked"`
	CIWaitSeconds  int64          `json:"ci_wait_seconds"`
	CIWaitInitial  int64          `json:"ci_wait_initial_seconds"`
	CIWaitRebase   int64          `json:"ci_wait_rebase_seconds"`
	CIWaitFix      int64          `json:"ci_wait_fix_seconds"`
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

// ActiveTaskFinder is the batched read side for tasks with work in flight.
// A task with no active run is absent from the returned map.
type ActiveTaskFinder interface {
	ActiveTaskIDs(ctx context.Context, taskIDs []string) (map[string]core.RunState, error)
}
