package core

import (
	"fmt"
	"strings"
	"time"
)

type RunID string

type RunState string

const (
	RunPending       RunState = "pending"
	RunRunning       RunState = "running"
	RunWaitingCI     RunState = "waiting_ci"
	RunNeedsRebase   RunState = "needs_rebase"
	RunNeedsAgentFix RunState = "needs_agent_fix"
	RunBlocked       RunState = "blocked"
	RunMerged        RunState = "merged"
	RunClosed        RunState = "closed"
)

func (s RunState) Terminal() bool { return s == RunMerged || s == RunClosed }

func (s RunState) ActiveAgent() bool { return s == RunRunning || s == RunNeedsAgentFix }

// LaunchSource names the path that started a run. It describes the caller, not
// a credential, so it is safe to serialize in snapshots and metrics.
type LaunchSource string

const (
	LaunchMCP    LaunchSource = "mcp"
	LaunchCLI    LaunchSource = "cli"
	LaunchUI     LaunchSource = "ui"
	LaunchDaemon LaunchSource = "daemon"
)

// ParseLaunchSource accepts a source token from MCP metadata, the CLI
// environment or an automatic path. Unknown and empty values resolve to
// daemon, the automatic path, so a caller cannot inject arbitrary labels into
// metrics.
func ParseLaunchSource(raw string) LaunchSource {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(LaunchMCP), "opencode":
		return LaunchMCP
	case string(LaunchCLI):
		return LaunchCLI
	case string(LaunchUI), "tui":
		return LaunchUI
	default:
		return LaunchDaemon
	}
}

type EventKind string

const (
	EvStart         EventKind = "start"
	EvPRRegistered  EventKind = "pr_registered"
	EvChecksPending EventKind = "checks_pending"
	EvChecksPassed  EventKind = "checks_passed"
	EvChecksFailed  EventKind = "checks_failed"
	EvBehind        EventKind = "behind"
	EvRebased       EventKind = "rebased"
	EvRebaseFailed  EventKind = "rebase_failed"
	EvAgentFixed    EventKind = "agent_fixed"
	EvBlock         EventKind = "block"
	EvUnblock       EventKind = "unblock"
	EvCancel        EventKind = "cancel"
	EvMerged        EventKind = "merged"
	EvClosed        EventKind = "closed"
)

type Event struct {
	Kind     EventKind
	HeadSHA  string
	PRNumber int
	Reason   string
}

var transitions = map[RunState]map[EventKind]RunState{
	RunPending:       {EvStart: RunRunning, EvCancel: RunClosed},
	RunRunning:       {EvPRRegistered: RunWaitingCI, EvChecksPending: RunWaitingCI, EvChecksPassed: RunWaitingCI, EvChecksFailed: RunNeedsAgentFix, EvBehind: RunNeedsRebase, EvBlock: RunBlocked, EvCancel: RunClosed, EvMerged: RunMerged},
	RunWaitingCI:     {EvChecksPending: RunWaitingCI, EvChecksPassed: RunWaitingCI, EvChecksFailed: RunNeedsAgentFix, EvBehind: RunNeedsRebase, EvBlock: RunBlocked, EvCancel: RunClosed, EvMerged: RunMerged},
	RunNeedsRebase:   {EvRebased: RunWaitingCI, EvRebaseFailed: RunNeedsAgentFix, EvBlock: RunBlocked, EvCancel: RunClosed},
	RunNeedsAgentFix: {EvAgentFixed: RunWaitingCI, EvChecksPassed: RunWaitingCI, EvBlock: RunBlocked, EvCancel: RunClosed, EvMerged: RunMerged},
	RunBlocked:       {EvUnblock: RunWaitingCI, EvCancel: RunClosed},
}

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

func (r *Run) CanRebase() bool { return r.RebaseAttempts < r.effectiveMax() }
func (r *Run) CanWake() bool   { return r.FixAttempts < r.effectiveMax() }

// Retry re-arms a closed run. A run that already has a PR returns to
// waiting_ci so the reconciler can finalize it. A run without a PR returns to
// pending with its session and worktree cleared, ready for PromotePending to
// spawn a fresh worker.
func (r *Run) Retry(reason string) error {
	if r.State != RunClosed {
		return &AppError{Code: CodeInvalidTransition, Message: fmt.Sprintf("cannot retry run %s in %s", r.ID, r.State)}
	}
	r.RebaseAttempts = 0
	r.FixAttempts = 0
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
	if obs.PRState == "merged" {
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
	r.RebaseAttempts = 0
	r.FixAttempts = 0
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
	case EvAgentFixed:
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
	}
	r.State = next
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// ObserveSHA records the PR head and, when supplied, the base SHA last seen for
// the run. It reports whether either value changed.
func (r *Run) ObserveSHA(sha string, base ...string) bool {
	if sha == "" {
		return false
	}
	baseSHA := r.ObservedBaseSHA
	if len(base) > 0 {
		baseSHA = base[0]
	}
	if sha == r.ObservedSHA && baseSHA == r.ObservedBaseSHA {
		return false
	}
	r.ObservedSHA = sha
	r.ObservedBaseSHA = baseSHA
	r.UpdatedAt = time.Now().UTC()
	return true
}
