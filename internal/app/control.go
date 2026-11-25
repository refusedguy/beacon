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

// Control applies operator actions to runs and persists the resulting state.
type Control struct {
	Store     core.RunStore
	Harness   core.AgentHarness
	Workspace core.WorkspaceManager
	OnChange  func(int)
	writeMu   sync.Mutex
}

// write serializes control mutations and retries transient SQLite lock
// conflicts. Read-only services never take this lock.
func (c *Control) write(ctx context.Context, fn func() error) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	var err error
	timer := time.NewTimer(controlRetryBase)
	timer.Stop()
	defer timer.Stop()
	for attempt := 0; attempt < controlWriteAttempts; attempt++ {
		err = fn()
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
// WatchPR reopens a run closed by workspace cleanup. The reconciler skips
// terminal runs, so a binding stored on a closed run is never finalized.
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
	if err := run.Apply(core.Event{Kind: core.EvPRRegistered, PRNumber: pr}); err != nil {
		switch {
		case run.State == core.RunWaitingCI:
			if err := run.MarkExternal(pr); err != nil {
				return err
			}
		case run.State == core.RunClosed && strings.HasPrefix(run.Reason, "workspace:"):
			bound, err := c.Store.FindByPR(ctx, pr)
			if err != nil {
				return err
			}
			if bound != nil && bound.ID != run.ID {
				return core.Errorf(core.CodeConflict, "PR %d belongs to run %s", pr, bound.ID)
			}
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
		if err := c.Harness.Stop(ctx, saved.SessionID); err != nil {
			slog.Error("cancel session stop", "run", saved.ID, "task", saved.TaskID, "error", err)
		}
	}
	if saved.WorkspacePath != "" {
		// The cancel is already saved. A failed remove leaves a worktree for the
		// next cleanup sweep, so it must not turn a successful cancel into an error.
		if err := c.Workspace.Remove(ctx, core.Workspace{Path: saved.WorkspacePath, Branch: saved.Branch}); err != nil {
			slog.Error("cancel workspace remove", "run", saved.ID, "task", saved.TaskID, "error", err)
		}
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

// Approve records a manual approval bound to the observed commit.
func (c *Control) Approve(ctx context.Context, id core.RunID, sha string) error {
	return c.write(ctx, func() error {
		run, err := c.get(ctx, id)
		if err != nil {
			return err
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
		switch run.State {
		case core.RunBlocked:
			if err := run.RetryBlocked("retried by operator"); err != nil {
				return err
			}
		case core.RunClosed:
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
	active, err := c.Store.FindActiveByTask(ctx, run.TaskID)
	if err != nil {
		return err
	}
	if active != nil && active.ID != run.ID {
		return core.Errorf(core.CodeConflict, "task %s already has active run %s", run.TaskID, active.ID)
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
