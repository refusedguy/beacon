package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// StatusArgs is the input schema for beacon_status.
type StatusArgs struct{}

func registerStatus(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_status"), func(ctx context.Context, _ *msdk.CallToolRequest, _ StatusArgs) (*msdk.CallToolResult, any, error) {
		runs, err := sv.Status.Active(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(core.NewWireRuns(runs)), nil, nil
	})
}

// RecentArgs is the input schema for beacon_recent.
type RecentArgs struct {
	Limit int `json:"limit"`
}

func registerRecent(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_recent"), func(ctx context.Context, _ *msdk.CallToolRequest, a RecentArgs) (*msdk.CallToolResult, any, error) {
		runs, err := sv.Status.Recent(ctx, a.Limit)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(core.NewWireRuns(runs)), nil, nil
	})
}
