package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

func registerTaskClose(s *msdk.Server, sv Services) {
	journal := newOpJournal(journalStore(sv))
	msdk.AddTool(s, catalogTool("beacon_task_close"), func(ctx context.Context, req *msdk.CallToolRequest, a closeTaskArgs) (*msdk.CallToolResult, any, error) {
		if err := requireOrchestrator(req); err != nil {
			return nil, nil, err
		}
		if err := requireService("task closer", sv.TaskUpdater); err != nil {
			return nil, nil, err
		}
		return mutationTool(ctx, journal, a.OperationID, func(ctx context.Context) (any, error) {
			return closeTasks(ctx, sv.TaskUpdater, a)
		}, taskGraphRecovery(sv))
	})
}

// closeTasks derives a closer from the wired updater. Both carry the same
// store, source, workspace and root, so the tool needs no extra service port.
func closeTasks(ctx context.Context, updater *app.TaskUpdater, a closeTaskArgs) (any, error) {
	closer := &app.TaskCloser{
		Store:     updater.Store,
		Source:    updater.Source,
		Workspace: updater.Workspace,
		Root:      updater.Root,
	}
	updates := make([]app.CloseInput, 0, len(a.Updates))
	for _, item := range a.Updates {
		updates = append(updates, app.CloseInput{
			ID:     item.ID,
			Status: core.TaskStatus(item.Status),
			Reason: item.Reason,
		})
	}
	return closer.Close(ctx, updates)
}

// closeTaskArgs is the input schema of beacon_task_close.
type closeTaskArgs struct {
	Updates     []closeOneArgs `json:"updates"`
	OperationID string         `json:"operation_id,omitempty"`
}

type closeOneArgs struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}
