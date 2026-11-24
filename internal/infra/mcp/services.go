package mcp

import (
	"context"

	"github.com/refusedguy/beacon/internal/app"
)

// Services holds the application services the MCP tools call.
type Services struct {
	Dispatcher  *app.Dispatcher
	Control     *app.Control
	Status      app.StatusService
	Wait        app.WaitService
	TaskCreator *app.TaskCreator
	TaskDeleter *app.TaskDeleter
	TaskUpdater *app.TaskUpdater
	Reconcile   *app.ReconcileService
	Remote      func(ctx context.Context, action, runID string, pr int, sha string) error
}
