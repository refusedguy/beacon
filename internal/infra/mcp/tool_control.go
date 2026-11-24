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

var controlTool = &msdk.Tool{
	Name:        "beacon_control",
	Description: "Control a run: approve, unblock, cancel, retry or bind a PR.",
}

func registerControl(s *msdk.Server, sv Services) {
	msdk.AddTool(s, controlTool, func(ctx context.Context, _ *msdk.CallToolRequest, a ControlArgs) (*msdk.CallToolResult, any, error) {
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
	return applyLocal(ctx, sv.Control, action, id, sha)
}

func applyLocal(ctx context.Context, control *app.Control, action, id, sha string) error {
	runID := core.RunID(id)
	switch action {
	case "approve":
		return control.Approve(ctx, runID, sha)
	case "unblock":
		return control.Unblock(ctx, runID)
	case "cancel":
		return control.Cancel(ctx, runID)
	case "retry":
		return control.Retry(ctx, runID)
	default:
		return core.Errorf(core.CodeInvalidInput, "unknown action %q", action)
	}
}

func controlMany(ctx context.Context, sv Services, a ControlArgs) (*msdk.CallToolResult, any, error) {
	if len(a.RunIDs) == 0 {
		return nil, nil, core.Errorf(core.CodeInvalidInput, "run_ids must not be empty")
	}
	if !validControlAction(a.Action) {
		return nil, nil, core.Errorf(core.CodeInvalidInput, "unknown action %q", a.Action)
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

func validControlAction(action string) bool {
	switch action {
	case "approve", "unblock", "cancel", "retry":
		return true
	default:
		return false
	}
}
