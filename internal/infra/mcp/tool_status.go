package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// StatusArgs is the input schema for beacon_status.
type StatusArgs struct{}

var statusTool = &msdk.Tool{
	Name:        "beacon_status",
	Description: "List active runs, PRs and blockers.",
}

func registerStatus(s *msdk.Server, sv Services) {
	msdk.AddTool(s, statusTool, func(ctx context.Context, _ *msdk.CallToolRequest, _ StatusArgs) (*msdk.CallToolResult, any, error) {
		runs, err := sv.Status.Active(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(runs), nil, nil
	})
}

// RecentArgs is the input schema for beacon_recent.
type RecentArgs struct {
	Limit int `json:"limit"`
}

var recentTool = &msdk.Tool{
	Name:        "beacon_recent",
	Description: "List the most recently finished runs.",
}

func registerRecent(s *msdk.Server, sv Services) {
	msdk.AddTool(s, recentTool, func(ctx context.Context, _ *msdk.CallToolRequest, a RecentArgs) (*msdk.CallToolResult, any, error) {
		runs, err := sv.Status.Recent(ctx, a.Limit)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(runs), nil, nil
	})
}
