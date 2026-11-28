package mcp

import (
	"context"
	"strings"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// StartArgs is the input schema for beacon_task_start. One ID names one card,
// so a call cannot widen into a batch that touches unrelated tasks.
type StartArgs struct {
	TaskID string `json:"task_id"`
}

func registerTaskStart(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_task_start"), func(ctx context.Context, req *msdk.CallToolRequest, a StartArgs) (*msdk.CallToolResult, any, error) {
		if err := requireOrchestrator(req); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(a.TaskID) == "" {
			return nil, nil, core.Errorf(core.CodeInvalidInput, "task_id is required")
		}
		if err := requireService("dispatcher", sv.Dispatcher); err != nil {
			return nil, nil, err
		}
		if parent := parentSession(req); parent != "" {
			ctx = app.WithParentSession(ctx, parent)
		}
		out, err := sv.Dispatcher.StartOne(ctx, core.TaskID(a.TaskID))
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(out), nil, nil
	})
}
