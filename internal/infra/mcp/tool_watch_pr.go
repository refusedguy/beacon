package mcp

import (
	"context"
	"fmt"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// WatchPRArgs is the input schema for beacon_watch_pr.
type WatchPRArgs struct {
	RunID string `json:"run_id"`
	PR    int    `json:"pr"`
}

func registerWatchPR(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_watch_pr"), func(ctx context.Context, _ *msdk.CallToolRequest, a WatchPRArgs) (*msdk.CallToolResult, any, error) {
		if sv.Remote != nil {
			if err := sv.Remote(ctx, "watch", a.RunID, a.PR, ""); err != nil {
				return nil, nil, err
			}
			return textResult("watching PR " + fmt.Sprint(a.PR)), nil, nil
		}
		if err := requireService("control service", sv.Control); err != nil {
			return nil, nil, err
		}
		if err := sv.Control.WatchPR(ctx, core.RunID(a.RunID), a.PR); err != nil {
			return nil, nil, err
		}
		return textResult("watching PR " + fmt.Sprint(a.PR)), nil, nil
	})
}
