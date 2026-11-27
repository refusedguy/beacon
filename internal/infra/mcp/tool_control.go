package mcp

import (
	"context"
	"strings"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// controlMessageKey carries the nudge message across the remote boundary. The
// Remote function signature predates the message field, so the wire adapter
// reads it from the call context instead of dropping it.
type controlMessageKey struct{}

func withControlMessage(ctx context.Context, message string) context.Context {
	return context.WithValue(ctx, controlMessageKey{}, message)
}

// ControlMessage returns the message bound by the MCP control tool. The
// loopback adapter uses it to build its request body.
func ControlMessage(ctx context.Context) string {
	message, _ := ctx.Value(controlMessageKey{}).(string)
	return message
}

// ControlArgs is the input schema for beacon_control. RunIDs is optional;
// when present it replaces RunID and the call returns one result per id.
type ControlArgs struct {
	Action  string   `json:"action,omitempty"`
	RunID   string   `json:"run_id,omitempty"`
	RunIDs  []string `json:"run_ids,omitempty"`
	SHA     string   `json:"sha,omitempty"`
	PR      int      `json:"pr,omitempty"`
	Message string   `json:"message,omitempty"`
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
	msdk.AddTool(s, catalogTool("beacon_control"), func(ctx context.Context, req *msdk.CallToolRequest, a ControlArgs) (*msdk.CallToolResult, any, error) {
		action := core.ParseControlAction(a.Action)
		if !action.Valid() {
			return nil, nil, core.Errorf(core.CodeInvalidInput, "unknown action %q", a.Action)
		}
		if a.RunID != "" && a.RunIDs != nil {
			return nil, nil, core.Errorf(core.CodeInvalidInput, "run_id and run_ids cannot be combined")
		}
		if action == core.ControlNudge && strings.TrimSpace(a.Message) == "" {
			return nil, nil, core.Errorf(core.CodeInvalidInput, "message is required for nudge")
		}
		ctx = app.WithControlActor(ctx, metaString(req, "beacon/agent", "ai.opencode/agent"))
		if a.RunIDs == nil {
			if err := applyControl(ctx, sv, a.Action, a.RunID, a.PR, a.SHA, a.Message); err != nil {
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
func applyControl(ctx context.Context, sv Services, action, id string, pr int, sha, message string) error {
	if sv.Remote != nil {
		return sv.Remote(withControlMessage(ctx, message), action, id, pr, sha)
	}
	if err := requireService("control service", sv.Control); err != nil {
		return err
	}
	return applyLocal(ctx, sv.Control, action, id, sha, pr, message)
}

// applyLocal hands the action to the shared app dispatch. Every advertised
// verb, including nudge, reaches the same implementation the other boundaries use.
func applyLocal(ctx context.Context, control *app.Control, action, id, sha string, pr int, message string) error {
	return control.Apply(ctx, core.ParseControlAction(action), app.ControlArgs{
		RunID: core.RunID(id), SHA: sha, PR: pr, Message: message,
	})
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
		if err := applyControl(ctx, sv, a.Action, id, a.PR, a.SHA, a.Message); err != nil {
			item.Status = "error"
			item.Error = err.Error()
		}
		results = append(results, item)
	}
	result := jsonResult(controlBatch{Action: a.Action, Results: results})
	for _, item := range results {
		if item.Status == "error" {
			result.IsError = true
			break
		}
	}
	return result, nil, nil
}
