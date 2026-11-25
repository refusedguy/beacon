package mcp

import (
	"context"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// WaitArgs is the input schema for beacon_wait.
type WaitArgs struct {
	RunID     string `json:"run_id"`
	TimeoutMS int    `json:"timeout_ms"`
}

func registerWait(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_wait"), func(ctx context.Context, _ *msdk.CallToolRequest, a WaitArgs) (*msdk.CallToolResult, any, error) {
		if a.TimeoutMS <= 0 || a.TimeoutMS > 600000 {
			a.TimeoutMS = 60000
		}
		cctx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutMS)*time.Millisecond)
		defer cancel()
		run, err := sv.Wait.WaitFor(cctx, core.RunID(a.RunID))
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(run), nil, nil
	})
}
