package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// ControlArgs is the input schema for beacon_control.
type ControlArgs struct {
	Action string `json:"action"`
	RunID  string `json:"run_id"`
	SHA    string `json:"sha"`
	PR     int    `json:"pr"`
}

var controlTool = &msdk.Tool{
	Name:        "beacon_control",
	Description: "Control a run: approve, unblock, cancel, retry or bind a PR.",
}

func registerControl(s *msdk.Server, sv Services) {
	msdk.AddTool(s, controlTool, func(ctx context.Context, _ *msdk.CallToolRequest, a ControlArgs) (*msdk.CallToolResult, any, error) {
		id := core.RunID(a.RunID)
		if sv.Remote != nil {
			if err := sv.Remote(ctx, a.Action, a.RunID, a.PR, a.SHA); err != nil {
				return nil, nil, err
			}
			return textResult("ok"), nil, nil
		}
		switch a.Action {
		case "approve":
			if err := sv.Control.Approve(ctx, id, a.SHA); err != nil {
				return nil, nil, err
			}
		case "unblock":
			if err := sv.Control.Unblock(ctx, id); err != nil {
				return nil, nil, err
			}
		case "cancel":
			if err := sv.Control.Cancel(ctx, id); err != nil {
				return nil, nil, err
			}
		case "retry":
			if err := sv.Control.Retry(ctx, id); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, core.Errorf(core.CodeInvalidInput, "unknown action %q", a.Action)
		}
		return textResult("ok"), nil, nil
	})
}
