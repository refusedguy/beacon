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

// maxWaitMS bounds a wait so a caller cannot hold an agent open indefinitely.
const maxWaitMS = 600000

func registerWait(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_wait"), func(ctx context.Context, _ *msdk.CallToolRequest, a WaitArgs) (*msdk.CallToolResult, any, error) {
		if a.TimeoutMS <= 0 || a.TimeoutMS > maxWaitMS {
			return nil, nil, core.Errorf(core.CodeInvalidInput,
				"timeout_ms must be between 1 and %d", maxWaitMS)
		}
		// WaitService is a value, so requireService cannot see an unwired
		// store through the interface check. Test the store directly instead.
		if sv.Wait.Store == nil {
			return nil, nil, core.Errorf(core.CodeUnavailable, "wait service is not configured")
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
