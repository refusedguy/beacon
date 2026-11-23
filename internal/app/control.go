package app

import (
	"context"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type Control struct {
	Store     RunStore
	Harness   AgentHarness
	Workspace WorkspaceManager
	OnChange  func(int)
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
			run.PRNumber = pr
		case run.State == core.RunClosed && strings.HasPrefix(run.Reason, "workspace:"):
			bound, err := c.Store.FindByPR(ctx, pr)
			if err != nil {
				return err
			}
			if bound != nil && bound.ID != run.ID {
				return core.Errorf(core.CodeConflict, "PR %d belongs to run %s", pr, bound.ID)
			}
			run.State = core.RunWaitingCI
			run.Reason = ""
			run.PRNumber = pr
			run.UpdatedAt = time.Now().UTC()
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

func (c *Control) Cancel(ctx context.Context, id core.RunID) error {
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
	if run.SessionID != "" {
		_ = c.Harness.Stop(ctx, run.SessionID)
	}
	if run.WorkspacePath != "" {
		_ = c.Workspace.Remove(ctx, Workspace{Path: run.WorkspacePath, Branch: run.Branch})
	}
	return nil
}

func (c *Control) Unblock(ctx context.Context, id core.RunID) error {
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
}

// Approve records a manual approval bound to the observed commit.
func (c *Control) Approve(ctx context.Context, id core.RunID, sha string) error {
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
}

// Retry re-arms an exhausted run by resetting attempt counters once.
func (c *Control) Retry(ctx context.Context, id core.RunID) error {
	run, err := c.get(ctx, id)
	if err != nil {
		return err
	}
	if run.State != core.RunBlocked {
		return core.Errorf(core.CodeConflict, "run %s is not blocked", id)
	}
	run.RebaseAttempts = 0
	run.FixAttempts = 0
	if err := run.Apply(core.Event{Kind: core.EvUnblock, Reason: "retried by operator"}); err != nil {
		return err
	}
	if err := c.Store.Save(ctx, run); err != nil {
		return err
	}
	c.signal(run)
	return nil
}

func (c *Control) signal(run *core.Run) {
	if c.OnChange != nil && run.PRNumber > 0 {
		c.OnChange(run.PRNumber)
	}
}
