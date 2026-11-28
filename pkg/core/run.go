package core

import (
	"fmt"
	"strings"
	"time"
)

// RunID identifies a run.
type RunID string

// RunState is the lifecycle state of a run.
type RunState string

const (
	// RunPending means the run awaits a worker to start.
	RunPending RunState = "pending"
	// RunRunning means an agent is working on the run.
	RunRunning RunState = "running"
	// RunWaitingCI means the run waits for pull request checks.
	RunWaitingCI RunState = "waiting_ci"
	// RunNeedsRebase means the branch fell behind the base and needs a rebase.
	RunNeedsRebase RunState = "needs_rebase"
	// RunNeedsAgentFix means an agent fix is required before the run can proceed.
	RunNeedsAgentFix RunState = "needs_agent_fix"
	// RunBlocked means an incident blocks the run.
	RunBlocked RunState = "blocked"
	// RunMerged means the run's pull request was merged.
	RunMerged RunState = "merged"
	// RunClosed means the run ended without a merge.
	RunClosed RunState = "closed"
)

// Terminal reports whether the state is terminal.
func (s RunState) Terminal() bool { return s == RunMerged || s == RunClosed }

// Blocked reports whether the state is blocked.
func (s RunState) Blocked() bool { return s == RunBlocked }

// ActiveAgent reports whether the state expects an active agent session.
func (s RunState) ActiveAgent() bool { return s == RunRunning || s == RunNeedsAgentFix }

// LaunchSource names the path that started a run. It describes the caller, not
// a credential, so it is safe to serialize in snapshots and metrics.
type LaunchSource string

const (
	// LaunchMCP means the run was started through an MCP tool.
	LaunchMCP LaunchSource = "mcp"
	// LaunchCLI means the run was started from the command line.
	LaunchCLI LaunchSource = "cli"
	// LaunchUI means the run was started from the monitor UI.
	LaunchUI LaunchSource = "ui"
	// LaunchDaemon means the run was started automatically by the daemon.
	LaunchDaemon LaunchSource = "daemon"
)

// ParseLaunchSource accepts a neutral source token from MCP metadata, the CLI
// environment or an automatic path. Callers normalize vendor-specific
// metadata to these tokens at the boundary before calling it. Unknown and
// empty values resolve to daemon, the automatic path, so a caller cannot inject
// arbitrary labels into metrics.
func ParseLaunchSource(raw string) LaunchSource {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(LaunchMCP):
		return LaunchMCP
	case string(LaunchCLI):
		return LaunchCLI
	case string(LaunchUI):
		return LaunchUI
	default:
		return LaunchDaemon
	}
}

// EventKind names a state transition event for a run.
type EventKind string

const (
	// EvStart starts a pending run.
	EvStart EventKind = "start"
	// EvPRRegistered records the pull request bound to a run.
	EvPRRegistered EventKind = "pr_registered"
	// EvChecksPending records that checks are still running.
	EvChecksPending EventKind = "checks_pending"
	// EvChecksPassed records that checks passed.
	EvChecksPassed EventKind = "checks_passed"
	// EvChecksFailed records that checks failed.
	EvChecksFailed EventKind = "checks_failed"
	// EvBehind records that the branch fell behind the base.
	EvBehind EventKind = "behind"
	// EvRebased records a successful rebase.
	EvRebased EventKind = "rebased"
	// EvRebaseFailed records a failed rebase.
	EvRebaseFailed EventKind = "rebase_failed"
	// EvAgentFixed records that an agent fixed the run.
	EvAgentFixed EventKind = "agent_fixed"
	// EvIdleNudge records that an idle worker was asked to open a PR. It keeps
	// the run in running state and spends one agent fix attempt.
	EvIdleNudge EventKind = "idle_nudge"
	// EvBlock records that an incident blocked the run.
	EvBlock EventKind = "block"
	// EvUnblock records that the blocking incident cleared.
	EvUnblock EventKind = "unblock"
	// EvCancel records a cancellation.
	EvCancel EventKind = "cancel"
	// EvMerged records that the pull request was merged.
	EvMerged EventKind = "merged"
	// EvClosed records that the run was closed without a merge.
	EvClosed EventKind = "closed"
)

// Event is a state transition request for a run.
type Event struct {
	Kind     EventKind
	HeadSHA  string
	PRNumber int
	Reason   string
}

var transitions = map[RunState]map[EventKind]RunState{
	RunPending:       {EvStart: RunRunning, EvCancel: RunClosed},
	RunRunning:       {EvIdleNudge: RunRunning, EvPRRegistered: RunWaitingCI, EvChecksPending: RunWaitingCI, EvChecksPassed: RunWaitingCI, EvChecksFailed: RunNeedsAgentFix, EvBehind: RunNeedsRebase, EvBlock: RunBlocked, EvCancel: RunClosed, EvMerged: RunMerged},
	RunWaitingCI:     {EvChecksPending: RunWaitingCI, EvChecksPassed: RunWaitingCI, EvChecksFailed: RunNeedsAgentFix, EvBehind: RunNeedsRebase, EvBlock: RunBlocked, EvCancel: RunClosed, EvMerged: RunMerged},
	RunNeedsRebase:   {EvRebased: RunWaitingCI, EvRebaseFailed: RunNeedsAgentFix, EvBlock: RunBlocked, EvCancel: RunClosed},
	RunNeedsAgentFix: {EvAgentFixed: RunWaitingCI, EvChecksPassed: RunWaitingCI, EvBlock: RunBlocked, EvCancel: RunClosed, EvMerged: RunMerged},
	RunBlocked:       {EvUnblock: RunWaitingCI, EvCancel: RunClosed},
}

// canTake reports whether transitions accepts kind from s. Each exported query
// names one event, so its eligible states stay tied to that event and cannot
// drift into another event's set.
func (s RunState) canTake(kind EventKind) bool {
	_, ok := transitions[s][kind]
	return ok
}

// CanTakeBehind reports whether s accepts EvBehind.
func (s RunState) CanTakeBehind() bool { return s.canTake(EvBehind) }

// CanTakeCheckFailed reports whether s accepts EvChecksFailed.
func (s RunState) CanTakeCheckFailed() bool { return s.canTake(EvChecksFailed) }

// CanTakeCheckPending reports whether s accepts EvChecksPending.
func (s RunState) CanTakeCheckPending() bool { return s.canTake(EvChecksPending) }

// Run is the full persisted state of one task execution.
type Run struct {
	ID                RunID
	TaskID            TaskID
	State             RunState
	PRNumber          int
	HeadSHA           string
	ObservedSHA       string
	ObservedBaseSHA   string
	SessionID         string
	ParentSessionID   string
	AgentIdentity     string
	LaunchSource      LaunchSource
	WorkspacePath     string
	Branch            string
	Scope             []ScopePath
	BlockedIncidentID string
	IsFixer           bool
	RebaseAttempts    int
	FixAttempts       int
	MaxAttempts       int
	Reason            string
	Checks            []CheckResult
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// WireRun is the loopback view of a run. The HTTP API and the status tools
// both emit it, so a monitor decodes one field set from either source.
type WireRun struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	PR        int    `json:"pr"`
	HeadSHA   string `json:"head_sha"`
	Branch    string `json:"branch"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
}

// NewWireRun maps a run into the loopback wire shape.
func NewWireRun(r *Run) WireRun {
	if r == nil {
		return WireRun{}
	}
	return WireRun{
		ID:        string(r.ID),
		TaskID:    string(r.TaskID),
		State:     string(r.State),
		PR:        r.PRNumber,
		HeadSHA:   r.HeadSHA,
		Branch:    r.Branch,
		Reason:    r.Reason,
		UpdatedAt: r.UpdatedAt.Format(time.RFC3339),
	}
}

// NewWireRuns maps runs in order. An empty input becomes an empty array, not
// null, so the monitor can always iterate the result.
func NewWireRuns(runs []*Run) []WireRun {
	out := make([]WireRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, NewWireRun(r))
	}
	return out
}

// WireEvent is one loopback notification. An event tells the monitor to
// re-read runs; it is not a source of run state.
type WireEvent struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	RunID string `json:"run_id"`
	At    string `json:"at"`
}

// NewWireEvent maps a harness event into the loopback wire shape. Harness
// events carry neither a sequence number nor a timestamp, so id and at stay
// empty. The client reads the event name from type.
func NewWireEvent(ev HarnessEvent) WireEvent {
	return WireEvent{Type: ev.Kind, RunID: string(ev.RunID)}
}

// WireControlResult is the loopback answer to a control request. Applied is
// true when the action was accepted by the daemon. Reason explains a refusal
// and is absent on an applied result.
type WireControlResult struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action"`
	RunID   string `json:"run_id"`
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
}

// NewWireControlResult reports an applied control action.
func NewWireControlResult(action ControlAction, id RunID) WireControlResult {
	return WireControlResult{OK: true, Action: action.String(), RunID: string(id), Applied: true}
}

// NewWireControlRefusal reports a control action the daemon refused. Reason is
// the daemon's explanation for the monitor's status line.
func NewWireControlRefusal(action ControlAction, id RunID, reason string) WireControlResult {
	return WireControlResult{OK: false, Action: action.String(), RunID: string(id), Applied: false, Reason: reason}
}

// NewRun creates a pending run with the default attempt budget.
func NewRun(id RunID, task TaskID, now time.Time) *Run {
	return &Run{ID: id, TaskID: task, State: RunPending, MaxAttempts: 3, CreatedAt: now, UpdatedAt: now}
}

// RecordLaunch attaches the caller to a run. The parent session keeps the
// orchestrator session; the agent name records who launched the run. Tokens
// have no place here.
func (r *Run) RecordLaunch(source LaunchSource, agent string) {
	r.LaunchSource = ParseLaunchSource(string(source))
	r.AgentIdentity = strings.TrimSpace(agent)
}

// RebaseBudgetExhausted reports whether the run has spent its rebase budget
// under cap. The run's own transitions pass effectiveMax, while the reconciler
// passes its smaller automatic-rebase cap. Keeping cap at the call site keeps
// both budgets separate.
func (r *Run) RebaseBudgetExhausted(cap int) bool { return r.RebaseAttempts >= cap }

// CanRebase reports whether the run has a rebase attempt left.
func (r *Run) CanRebase() bool { return !r.RebaseBudgetExhausted(r.effectiveMax()) }

// CanWake reports whether the run has an agent fix attempt left.
func (r *Run) CanWake() bool { return r.FixAttempts < r.effectiveMax() }

// ResetAttempts clears the rebase and fix counters. Re-arming a run calls this
// so no path clears the counters one field at a time.
func (r *Run) ResetAttempts() {
	r.RebaseAttempts = 0
	r.FixAttempts = 0
}

// Retry re-arms a closed run. A run that already has a PR returns to
// waiting_ci so the reconciler can finalize it. A run without a PR returns to
// pending with its session and worktree cleared, ready for PromotePending to
// spawn a fresh worker.
func (r *Run) Retry(reason string) error {
	if r.State != RunClosed {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot retry run %s in %s", r.ID, r.State)}
	}
	r.ResetAttempts()
	if r.PRNumber > 0 {
		r.State = RunWaitingCI
	} else {
		r.State = RunPending
		r.SessionID = ""
		r.WorkspacePath = ""
	}
	r.Reason = reason
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// ExternalFinalize moves a run to a terminal state after its PR was merged or
// closed outside Beacon. It keeps the PR number and records the observed SHA.
func (r *Run) ExternalFinalize(obs Observation) error {
	if r.State.Terminal() {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("run %s is already %s", r.ID, r.State)}
	}
	if DecideFinalState(obs.PRState).Merged {
		r.State = RunMerged
		r.Reason = fmt.Sprintf("merged PR #%d via squash", obs.PRNumber)
	} else {
		r.State = RunClosed
		r.Reason = "pull request " + obs.PRState
	}
	if obs.HeadSHA != "" {
		r.HeadSHA = obs.HeadSHA
	}
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// ReopenWithPR reopens a run closed by workspace cleanup and binds the PR that
// arrived after the cleanup. The reconciler can then finalize the run.
func (r *Run) ReopenWithPR(pr int) error {
	if r.State != RunClosed {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot reopen run %s in %s", r.ID, r.State)}
	}
	r.State = RunWaitingCI
	r.Reason = ""
	r.PRNumber = pr
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// RetryBlocked re-arms a blocked run by clearing its attempt counters and
// returning it to waiting_ci.
func (r *Run) RetryBlocked(reason string) error {
	if r.State != RunBlocked {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot retry run %s in %s", r.ID, r.State)}
	}
	r.ResetAttempts()
	return r.Apply(Event{Kind: EvUnblock, Reason: reason})
}

// MarkExternal records a PR opened outside the run's own spawn path without
// changing the run's state.
func (r *Run) MarkExternal(pr int) error {
	if r.State.Terminal() {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot mark PR on run %s in %s", r.ID, r.State)}
	}
	r.PRNumber = pr
	r.UpdatedAt = time.Now().UTC()
	return nil
}

func (r *Run) effectiveMax() int {
	if r.MaxAttempts <= 0 {
		return 3
	}
	return r.MaxAttempts
}

// Apply transitions the run using the given event.
func (r *Run) Apply(e Event) error {
	if r.State.Terminal() {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("run %s is already %s", r.ID, r.State)}
	}
	next, ok := transitions[r.State][e.Kind]
	if !ok {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot %s from %s", e.Kind, r.State)}
	}
	switch e.Kind {
	case EvRebased:
		if !r.CanRebase() {
			return &AppError{Code: CodeExhausted, Message: "rebase attempts exhausted"}
		}
		r.RebaseAttempts++
	case EvAgentFixed, EvIdleNudge:
		if !r.CanWake() {
			return &AppError{Code: CodeExhausted, Message: "agent fix attempts exhausted"}
		}
		r.FixAttempts++
	}
	if e.HeadSHA != "" {
		r.HeadSHA = e.HeadSHA
	}
	if e.PRNumber != 0 {
		r.PRNumber = e.PRNumber
	}
	if e.Reason != "" {
		r.Reason = e.Reason
	} else if e.Kind == EvAgentFixed || e.Kind == EvChecksPassed || e.Kind == EvRebased {
		// These events mark a recovered run, so a prior failure reason is no
		// longer current. Preserve an explicit reason when one is supplied.
		r.Reason = ""
	}
	r.State = next
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// NoteObservation records the PR head and base SHA last seen for the run and
// reports whether either value changed. It is the single place that stamps an
// observation, so head, base and timestamp always move together.
func (r *Run) NoteObservation(headSHA, baseSHA string) bool {
	if headSHA == r.ObservedSHA && baseSHA == r.ObservedBaseSHA {
		return false
	}
	r.ObservedSHA = headSHA
	r.ObservedBaseSHA = baseSHA
	r.UpdatedAt = time.Now().UTC()
	return true
}

// ObserveSHA records the PR head and, when supplied, the base SHA last seen for
// the run. It reports whether either value changed. An empty head is ignored.
func (r *Run) ObserveSHA(sha string, base ...string) bool {
	if sha == "" {
		return false
	}
	baseSHA := r.ObservedBaseSHA
	if len(base) > 0 {
		baseSHA = base[0]
	}
	return r.NoteObservation(sha, baseSHA)
}
