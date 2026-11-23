package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
)

// DispatchArgs is the input schema for beacon_dispatch.
type DispatchArgs struct {
	N int `json:"n"`
}

var dispatchTool = &msdk.Tool{
	Name:        "beacon_dispatch",
	Description: "Pick up to N non-overlapping tasks and start agents.",
}

func registerDispatch(s *msdk.Server, sv Services) {
	msdk.AddTool(s, dispatchTool, func(ctx context.Context, req *msdk.CallToolRequest, a DispatchArgs) (*msdk.CallToolResult, any, error) {
		if a.N <= 0 {
			a.N = 1
		}
		if parent := parentSession(req); parent != "" {
			ctx = app.WithParentSession(ctx, parent)
		}
		res, err := sv.Dispatcher.Dispatch(ctx, a.N)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(map[string]any{
			"runs":     res.Runs,
			"skipped":  res.Skipped,
			"failures": res.Failures,
		}), nil, nil
	})
}

func parentSession(req *msdk.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	for _, key := range []string{"ai.opencode/sessionID", "beacon/sessionID"} {
		if v, ok := req.Params.Meta[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}
