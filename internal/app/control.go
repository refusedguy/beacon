package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// bestEffort runs a cleanup operation and logs a failure without returning it.
// Callers use it when the main work is already saved and a later sweep can
// retry the cleanup, so a failed cleanup must not fail the operation.
func bestEffort(what string, fn func() error, attrs ...any) {
	if err := fn(); err != nil {
		slog.Error(what, append(attrs, "error", err)...)
	}
}

const (
	controlWriteAttempts = 3
	controlRetryBase     = 10 * time.Millisecond

	// sqliteBusyCode is the SQLite SQLITE_BUSY result code. It belongs to the
	// SQLite API, so a driver can reword its message without changing it.
	sqliteBusyCode = 5
)

// sqliteCodedError is the shape of a driver error that carries a result code.
// Declaring it here keeps the app layer independent of a concrete driver.
type sqliteCodedError interface {
	error
	Code() int
}

// ControlStore is the store surface Control uses to apply one operator action.
// Control reads one run, checks a PR owner, persists state and records an
// approval. The active-run lookup that Retry needs stays an optional capability
// resolved by activeTaskStates, the same way the reconciler type-asserts the
// retention sweep it only runs on a daemon tick.
type ControlStore interface {
	Save(ctx context.Context, run *core.Run) error
	Get(ctx context.Context, id core.RunID) (*core.Run, error)
	FindByPR(ctx context.Context, pr int) (*core.Run, error)
	SetApproval(ctx context.Context, id core.RunID, sha string) error
}

// Control applies operator actions to runs and persists the resulting state.
type Control struct {
	Store     ControlStore
	Harness   core.AgentHarness
	Workspace core.WorkspaceManager
	OnChange  func(int)
	// AgentSlots is optional. When the port supports atomic reservations,
	// Nudge reserves a slot before waking so two nudges cannot spend the same
	// free slot.
	AgentSlots core.AgentSlots
	writeMu    sync.Mutex
}

// ControlArgs carries the arguments of one control action. A boundary fills
// only the fields its verb uses. The shared dispatch ignores the rest.
type ControlArgs struct {
	RunID   core.RunID
	SHA     string
	PR      int
	Message string
}

// ControlNotice reports a control action that needs no change because the run
// is already in the requested state. Boundaries present it as success with the
// reason attached, so a retried batch does not flip an applied id to an error.
type ControlNotice struct {
	Reason string
}

func (n *ControlNotice) Error() string { return n.Reason }

// slotReserver reports the richer slot policy when the injected AgentSlots
// supports reservations. Other ports keep the plain availability check.
func (c *Control) slotReserver() wakeSlots {
	slots, _ := c.AgentSlots.(wakeSlots)
	return slots
}

// controlActorKey carries the agent that initiated a control action. A
// boundary sets it from caller metadata without changing the ControlPort
// methods. Control uses it to reject a worker that acts on its own run.
type controlActorKey struct{}

// WithControlActor binds the acting agent to ctx. An empty agent is allowed:
// boundaries that cannot identify their caller keep the existing behaviour.
func WithControlActor(ctx context.Context, agent string) context.Context {
	return context.WithValue(ctx, controlActorKey{}, strings.TrimSpace(agent))
}

// ControlActor returns the acting agent bound by WithControlActor.
func ControlActor(ctx context.Context) string {
	actor, _ := ctx.Value(controlActorKey{}).(string)
	return actor
}

// isWorkerAgent recognizes the worker role and its configured aliases without
// treating every name that contains "worker" as a worker.
func isWorkerAgent(agent string) bool {
	name := strings.ToLower(strings.TrimSpace(agent))
	return name == "worker" || strings.HasSuffix(name, "-worker")
}

// ControlPort is the slice of Control that every boundary drives. Each method
// maps to one ControlAction, so a port needs no verb-specific switch of its own.
type ControlPort interface {
	WatchPR(ctx context.Context, id core.RunID, pr int) error
	Approve(ctx context.Context, id core.RunID, sha string) error
	Unblock(ctx context.Context, id core.RunID) error
	Cancel(ctx context.Context, id core.RunID) error
	Retry(ctx context.Context, id core.RunID) error
	Nudge(ctx context.Context, id core.RunID, message string) error
}

// ApplyControl routes one action through port. This is the only switch over the
// control vocabulary, so adding a verb here makes it available at every boundary.
func ApplyControl(ctx context.Context, port ControlPort, action core.ControlAction, args ControlArgs) error {
	switch action {
	case core.ControlApprove:
		return port.Approve(ctx, args.RunID, args.SHA)
	case core.ControlUnblock:
		return port.Unblock(ctx, args.RunID)
	case core.ControlCancel:
		return port.Cancel(ctx, args.RunID)
	case core.ControlRetry:
		return port.Retry(ctx, args.RunID)
	case core.ControlNudge:
		return port.Nudge(ctx, args.RunID, args.Message)
	case core.ControlWatch:
		return port.WatchPR(ctx, args.RunID, args.PR)
	default:
		return core.Errorf(core.CodeInvalidInput, "unknown action %q", action)
	}
}

// Apply routes one action through c. The CLI, the loopback API and the MCP
// tool all call this single port instead of switching over verbs themselves.
func (c *Control) Apply(ctx context.Context, action core.ControlAction, args ControlArgs) error {
	return ApplyControl(ctx, c, action, args)
}

// write serializes control mutations and retries transient SQLite lock
// conflicts. Read-only services never take this lock. The lock covers one
// store call at a time, never a backoff sleep, so concurrent writers do not
// serialize each other's retries.
func (c *Control) write(ctx context.Context, fn func() error) error {
	var err error
	timer := time.NewTimer(controlRetryBase)
	timer.Stop()
	defer timer.Stop()
	for attempt := 0; attempt < controlWriteAttempts; attempt++ {
		err = c.writeOnce(fn)
		if !isBusy(err) {
			return err
		}
		if attempt+1 < controlWriteAttempts {
			timer.Reset(controlRetryBase << attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}

// writeOnce runs a single mutation attempt under the write lock. The lock is
// released before the caller backs off, keeping it as short as the store call.
func (c *Control) writeOnce(fn func() error) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return fn()
}

// isBusy recognises SQLite write-lock conflicts. A typed driver error gives
// the authoritative result code, so retries survive a driver message change.
// The fallback covers errors that expose no code; it matches the canonical
// marker exactly instead of lowercasing the whole message.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	var coded sqliteCodedError
	if errors.As(err, &coded) {
		return coded.Code() == sqliteBusyCode
	}
	return strings.Contains(err.Error(), "(SQLITE_BUSY)")
}

func (c *Control) get(ctx context.Context, id core.RunID) (*core.Run, error) {
	run, err := c.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, core.Errorf(core.CodeNotFound, "run %s", id)
	}
	return run, nil
}

// WatchPR binds a pull request to a run and subscribes it to reconciliation.
// A running run takes the usual pr_registered transition. Every other
// nonterminal state binds with MarkExternal so the operator can rebind a PR
// after a check failure, a rebase or a block. WatchPR also reopens a run closed
// by workspace cleanup. The reconciler skips terminal runs, so a binding stored
// on a closed run is never finalized.
func (c *Control) WatchPR(ctx context.Context, id core.RunID, pr int) error {
	return c.write(ctx, func() error { return c.watchPR(ctx, id, pr) })
}

func (c *Control) watchPR(ctx context.Context, id core.RunID, pr int) error {
	run, err := c.get(ctx, id)
	if err != nil {
		return err
	}
	if pr <= 0 {
		return core.Errorf(core.CodeInvalidInput, "pr must be positive")
	}
	if run.PRNumber != 0 && run.PRNumber != pr {
		return core.Errorf(core.CodeConflict, "run %s already watches PR %d", id, run.PRNumber)
	}
	if run.PRNumber == 0 {
		bound, err := c.Store.FindByPR(ctx, pr)
		if err != nil {
			return err
		}
		if bound != nil && bound.ID != run.ID {
			return core.Errorf(core.CodeConflict, "PR %d belongs to run %s", pr, bound.ID)
		}
	}
	if err := run.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: pr}); err != nil {
		switch {
		case !run.State.Terminal():
			if err := run.MarkExternal(pr); err != nil {
				return err
			}
		case run.State == core.RunClosed && strings.HasPrefix(run.Reason, "workspace:"):
			if err := run.ReopenWithPR(pr); err != nil {
				return err
			}
		default:
			return err
		}
	}
	if err := c.Store.Save(ctx, run); err != nil {
		return err
	}
	c.signal(run)
	return nil
}

// Cancel closes a run, then stops its worker session and removes its workspace.
// Cleanup failures are logged and do not undo the saved cancellation.
func (c *Control) Cancel(ctx context.Context, id core.RunID) error {
	var saved *core.Run
	err := c.write(ctx, func() error {
		run, err := c.get(ctx, id)
		if err != nil {
			return err
		}
		if run.State == core.RunClosed {
			// The cancel is already applied. A repeat is a no-op so a retried
			// batch converges instead of reporting invalid_transition.
			return &ControlNotice{Reason: "already_closed"}
		}
		if err := run.Apply(core.Event{Kind: core.EvCancel, Reason: "cancelled by operator"}); err != nil {
			return err
		}
		if err := c.Store.Save(ctx, run); err != nil {
			return err
		}
		saved = run
		return nil
	})
	if err != nil {
		return err
	}
	if saved.SessionID != "" {
		bestEffort("cancel session stop", func() error {
			return c.Harness.Stop(ctx, saved.SessionID)
		}, "run", saved.ID, "task", saved.TaskID)
	}
	if saved.WorkspacePath != "" {
		// The cancel is already saved. A failed remove leaves a worktree for the
		// next cleanup sweep, so it must not turn a successful cancel into an error.
		bestEffort("cancel workspace remove", func() error {
			return c.Workspace.Remove(ctx, core.Workspace{Path: saved.WorkspacePath, Branch: saved.Branch})
		}, "run", saved.ID, "task", saved.TaskID)
	}
	return nil
}

// Unblock moves a blocked run back to waiting_ci.
func (c *Control) Unblock(ctx context.Context, id core.RunID) error {
	return c.write(ctx, func() error {
		run, err := c.get(ctx, id)
		if err != nil {
			return err
		}
		if err := run.Apply(core.Event{Kind: core.EvUnblock, Reason: "unblocked by operator"}); err != nil {
			return err
		}
		if err := c.Store.Save(ctx, run); err != nil {
			return err
		}
		c.signal(run)
		return nil
	})
}

// Approve records a manual approval bound to the observed commit. Approval
// needs a run that is not terminal and has no active agent, so a worker cannot
// approve the run it is executing. A caller that identifies itself as the
// worker that launched the run is refused in every state.
func (c *Control) Approve(ctx context.Context, id core.RunID, sha string) error {
	return c.write(ctx, func() error {
		run, err := c.get(ctx, id)
		if err != nil {
			return err
		}
		if run.State.Terminal() || run.State.ActiveAgent() {
			return core.Errorf(core.CodeConflict, "run %s is %s; approve needs an idle run", id, run.State)
		}
		actor := ControlActor(ctx)
		if isWorkerAgent(actor) && actor == run.AgentIdentity {
			return core.Errorf(core.CodeConflict, "worker %s cannot approve its own run %s", actor, id)
		}
		if sha == "" {
			sha = run.HeadSHA
		}
		if sha == "" {
			return core.Errorf(core.CodeInvalidInput, "no head SHA for run %s", id)
		}
		if err := c.Store.SetApproval(ctx, id, sha); err != nil {
			return err
		}
		c.signal(run)
		return nil
	})
}

// Retry re-arms an exhausted run or a closed run that has no replacement.
// Blocked runs reset their attempt counters and return to waiting_ci. Closed
// runs keep their PR when they have one, otherwise they return to pending so
// the dispatcher can spawn a fresh worker.
func (c *Control) Retry(ctx context.Context, id core.RunID) error {
	return c.write(ctx, func() error {
		run, err := c.get(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case run.State.Blocked():
			if err := run.RetryBlocked("retried by operator"); err != nil {
				return err
			}
		case run.State == core.RunClosed:
			if err := c.retryClosed(ctx, run); err != nil {
				return err
			}
		default:
			return core.Errorf(core.CodeConflict, "run %s is not blocked or closed", id)
		}
		if err := c.Store.Save(ctx, run); err != nil {
			return err
		}
		c.signal(run)
		return nil
	})
}

func (c *Control) retryClosed(ctx context.Context, run *core.Run) error {
	task := string(run.TaskID)
	active, err := activeTaskStates(ctx, c.Store, []string{task})
	if err != nil {
		return err
	}
	if _, ok := active[task]; ok {
		return core.Errorf(core.CodeConflict, "task %s already has an active run", run.TaskID)
	}
	if err := run.Retry("retried by operator"); err != nil {
		return err
	}
	return nil
}

// Nudge sends operator guidance to the live session of an active run.
func (c *Control) Nudge(ctx context.Context, id core.RunID, message string) error {
	run, err := c.get(ctx, id)
	if err != nil {
		return err
	}
	if run.State.Terminal() {
		return core.Errorf(core.CodeConflict, "run %s is %s", id, run.State)
	}
	if run.SessionID == "" {
		return core.Errorf(core.CodeConflict, "run %s has no session", id)
	}
	if message == "" {
		return core.Errorf(core.CodeInvalidInput, "message is required")
	}
	if c.Harness == nil {
		return core.Errorf(core.CodeConflict, "harness is not configured")
	}
	if slots := c.slotReserver(); slots != nil {
		if !slots.ReserveForWake(ctx, run.ID) {
			return core.Errorf(core.CodeConflict, "no agent slot free for run %s", id)
		}
		defer slots.ReleaseWake(ctx, run.ID)
	} else if c.AgentSlots != nil && c.AgentSlots.Available(ctx) <= 0 {
		return core.Errorf(core.CodeConflict, "no agent slot free for run %s", id)
	}
	// A nudge keeps the existing session live, so it holds the run's slot.
	run.HoldAgent()
	if err := c.Store.Save(ctx, run); err != nil {
		return err
	}
	return c.Harness.Wake(ctx, run.SessionID, core.WakeContext{
		RunID: run.ID, PRNumber: run.PRNumber, HeadSHA: run.HeadSHA,
		Reason: "operator guidance", Excerpt: message,
		Workspace: run.WorkspacePath, Branch: run.Branch,
	})
}

func (c *Control) signal(run *core.Run) {
	if c.OnChange != nil && run.PRNumber > 0 {
		c.OnChange(run.PRNumber)
	}
}
