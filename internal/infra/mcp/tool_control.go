package mcp

import (
	"context"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// ControlArgs is the input schema for beacon_control. RunIDs is optional;
// when present it replaces RunID and the call returns one result per id.
type ControlArgs struct {
	Action string   `json:"action,omitempty"`
	RunID  string   `json:"run_id,omitempty"`
	RunIDs []string `json:"run_ids,omitempty"`
	SHA    string   `json:"sha,omitempty"`
	PR     int      `json:"pr,omitempty"`
}

type controlItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type controlBatch struct {
	Action  string        `json:"action"`
	Results []controlItem `json:"results"`
}

func registerControl(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_control"), func(ctx context.Context, _ *msdk.CallToolRequest, a ControlArgs) (*msdk.CallToolResult, any, error) {
		if a.RunIDs == nil {
			if err := applyControl(ctx, sv, a.Action, a.RunID, a.PR, a.SHA); err != nil {
				return nil, nil, err
			}
			return textResult("ok"), nil, nil
		}
		return controlMany(ctx, sv, a)
	})
}

// applyControl forwards one action either to the loopback API or to the local
// control service. The loopback API accepts one run per request, so batches
// are sent sequentially and each id keeps its own outcome.
func applyControl(ctx context.Context, sv Services, action, id string, pr int, sha string) error {
	if sv.Remote != nil {
		return sv.Remote(ctx, action, id, pr, sha)
	}
	if err := requireService("control service", sv.Control); err != nil {
		return err
	}
	return applyLocal(ctx, sv.Control, action, id, sha, pr)
}

func applyLocal(ctx context.Context, control *app.Control, action, id, sha string, pr int) error {
	runID := core.RunID(id)
	switch core.ParseControlAction(action) {
	case core.ControlApprove:
		return control.Approve(ctx, runID, sha)
	case core.ControlUnblock:
		return control.Unblock(ctx, runID)
	case core.ControlCancel:
		return control.Cancel(ctx, runID)
	case core.ControlRetry:
		return control.Retry(ctx, runID)
	case core.ControlWatch:
		return control.WatchPR(ctx, runID, pr)
	default:
		return core.Errorf(core.CodeInvalidInput, "unknown action %q", action)
	}
}

func controlMany(ctx context.Context, sv Services, a ControlArgs) (*msdk.CallToolResult, any, error) {
	if len(a.RunIDs) == 0 {
		return nil, nil, core.Errorf(core.CodeInvalidInput, "run_ids must not be empty")
	}
	if !core.ParseControlAction(a.Action).Valid() {
		return nil, nil, core.Errorf(core.CodeInvalidInput, "unknown action %q", a.Action)
	}
	if err := requireService("control service", sv.Remote, sv.Control); err != nil {
		return nil, nil, err
	}
	results := make([]controlItem, 0, len(a.RunIDs))
	for _, id := range a.RunIDs {
		item := controlItem{ID: id, Status: "ok"}
		if err := applyControl(ctx, sv, a.Action, id, a.PR, a.SHA); err != nil {
			item.Status = "error"
			item.Error = err.Error()
		}
		results = append(results, item)
	}
	return jsonResult(controlBatch{Action: a.Action, Results: results}), nil, nil
}
