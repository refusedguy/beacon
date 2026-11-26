package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// PathCommitter is the part of the workspace port that publishes a new card.
type PathCommitter interface {
	CommitPath(ctx context.Context, ws core.Workspace, pathspec, message string) error
}

// BaseRebaser is an optional workspace capability. GitWorktree implements it
// so publishing a card syncs a stale local base with origin before pushing.
// Fakes that implement it let tests observe the rebase and the push retry.
type BaseRebaser interface {
	RebaseBase(ctx context.Context, ws core.Workspace, base string) error
}

// BranchEnsurer is an optional workspace capability. GitWorktree implements it
// so an epic branch exists on origin before any child of that epic dispatches.
type BranchEnsurer interface {
	EnsureBranch(ctx context.Context, branch, base string) error
}

// CardPublisher commits one pathspec. The workspace and root are bound at
// construction, so a caller names only what changed and why.
type CardPublisher interface {
	Publish(ctx context.Context, pathspec, message string) error
}

type pathCardPublisher struct {
	ws   PathCommitter
	root core.Workspace
}

// Publish commits pathspec in the bound workspace using the given message.
func (p pathCardPublisher) Publish(ctx context.Context, pathspec, message string) error {
	return publishPath(ctx, p.ws, p.root, pathspec, message)
}

// NonFastForwardError marks a push rejected because the remote ref advanced.
// A caller retries the publish once after rebasing on the latest origin base.
type NonFastForwardError struct{ Err error }

// Error returns the rejection message, including the wrapped push error when set.
func (e *NonFastForwardError) Error() string {
	if e.Err == nil {
		return "push rejected as non-fast-forward"
	}
	return "push rejected as non-fast-forward: " + e.Err.Error()
}

// Unwrap returns the underlying push error.
func (e *NonFastForwardError) Unwrap() error { return e.Err }

// detachedPushTimeout bounds a push that must finish after the caller's
// context was cancelled. A committed card must not stay unpublished.
const detachedPushTimeout = 30 * time.Second

// publishPath rebases onto the configured base, then commits pathspec. A push
// rejected as non-fast-forward is retried once on the same base. Cancellation
// before the commit aborts the publish so the caller can restore the cards.
// Cancellation after the commit cannot undo it, so the retry finishes with a
// bounded context detached from the caller.
func publishPath(ctx context.Context, ws PathCommitter, root core.Workspace, pathspec, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := publishBase(root)
	if err := rebaseBase(ctx, ws, root, base); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publishWithNFFRetry(ctx, ws, root, base, pathspec, message)
}

// publishWithNFFRetry commits pathspec and retries the push once when the
// remote ref advanced. Task creation and the reconciler share it. The caller
// chooses the rebase base: task creation passes publishBase(root), the
// reconciler passes the run's own branch. Keeping the base at the call site
// makes it clear why a retry rebased onto a particular ref. The retry itself
// runs under the bounded context in retryPublish.
func publishWithNFFRetry(ctx context.Context, ws PathCommitter, root core.Workspace, base, pathspec, message string) error {
	if err := ws.CommitPath(ctx, root, pathspec, message); err != nil {
		var nff *NonFastForwardError
		if !errors.As(err, &nff) {
			return err
		}
		return retryPublish(ctx, ws, root, pathspec, message, base)
	}
	return nil
}

func publishBase(root core.Workspace) string {
	if root.Base != "" {
		return root.Base
	}
	if root.Branch != "" {
		return root.Branch
	}
	return "main"
}

// retryPublish completes a push rejected as non-fast-forward. A cancelled
// caller must not leave a committed card unpublished, so the retry uses a
// detached bounded context.
func retryPublish(ctx context.Context, ws PathCommitter, root core.Workspace, pathspec, message, base string) error {
	opCtx, cancel := detachedContext(ctx)
	defer cancel()
	if err := rebaseBase(opCtx, ws, root, base); err != nil {
		return err
	}
	return ws.CommitPath(opCtx, root, pathspec, message)
}

// detachedContext returns a bounded context that keeps the caller's values but
// cannot be cancelled with it. Callers use it only after a commit is durable.
// Checking ctx.Err() once is not enough: a cancellation arriving mid-operation
// would still abort a push or rollback that must finish.
func detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), detachedPushTimeout)
}

func rebaseBase(ctx context.Context, ws PathCommitter, root core.Workspace, base string) error {
	if rebaser, ok := ws.(BaseRebaser); ok {
		return rebaser.RebaseBase(ctx, root, base)
	}
	return nil
}

// ensureBranch publishes an epic base branch when the workspace supports it.
// Tasks on the configured base need no branch work, and a workspace without the
// capability keeps its previous behavior.
func ensureBranch(ctx context.Context, ws any, branch, base string) error {
	branch = strings.TrimSpace(branch)
	if !strings.HasPrefix(branch, epicBranchPrefix) {
		return nil
	}
	ensurer, ok := ws.(BranchEnsurer)
	if !ok {
		return nil
	}
	return ensurer.EnsureBranch(ctx, branch, base)
}
