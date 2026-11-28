package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
// OperationID journals a batch so a retry returns the stored per-id outcomes.
type ControlArgs struct {
	Action      string   `json:"action,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	RunIDs      []string `json:"run_ids,omitempty"`
	SHA         string   `json:"sha,omitempty"`
	PR          int      `json:"pr,omitempty"`
	Message     string   `json:"message,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
}

type controlItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type controlBatch struct {
	Action  string        `json:"action"`
	Results []controlItem `json:"results"`
	app.OperationMeta
}

// WithMeta lets the mutation journal stamp idempotency metadata on a batch
// without knowing the concrete result type.
func (b controlBatch) WithMeta(id, status string) any {
	b.Status, b.OperationID = status, id
	return b
}

// hasError reports whether any id failed. One failed id makes the whole call
// an error even when the other ids succeeded.
func (b controlBatch) hasError() bool {
	for _, item := range b.Results {
		if item.Status == "error" {
			return true
		}
	}
	return false
}

// controlOperationStore returns the durable journal store behind the control
// service. Control shares the state store with the task mutations, so a batch
// replay survives a restart. Without a durable store the journal stays in memory.
func controlOperationStore(sv Services) operationStore {
	if sv.Control != nil {
		if store, ok := sv.Control.Store.(operationStore); ok && store != nil {
			return store
		}
	}
	return journalStore(sv)
}

func registerControl(s *msdk.Server, sv Services) {
	journal := newOpJournal(controlOperationStore(sv))
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
				if _, ok := noticeReason(err); ok {
					return textResult("ok"), nil, nil
				}
				if refusal, ok := decodeControlRefusal(a.Action, a.RunID, err); ok {
					result := jsonResult(refusal)
					result.IsError = true
					return result, nil, nil
				}
				return nil, nil, err
			}
			return textResult("ok"), nil, nil
		}
		return controlMany(ctx, journal, sv, a)
	})
}

// noticeReason reports a control action that needed no change. Boundaries
// present it as success with the reason attached so a retry converges.
func noticeReason(err error) (string, bool) {
	var notice *app.ControlNotice
	if errors.As(err, &notice) {
		return notice.Reason, true
	}
	return "", false
}

// controlRefusalReason recovers the daemon's reason from a refused control
// call. The loopback adapter turns a 409 wire refusal into a formatted error,
// so the reason lives in the error text. A refusal without a reason is not a
// structured refusal and stays a plain error.
func controlRefusalReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	const prefix = "beacon API status "
	msg := err.Error()
	if !strings.HasPrefix(msg, prefix) {
		return "", false
	}
	rest := msg[len(prefix):]
	i := strings.Index(rest, ": ")
	if i < 0 {
		return "", false
	}
	reason := strings.TrimSpace(rest[i+2:])
	return reason, reason != ""
}

// decodeControlRefusal rebuilds the wire refusal from the action and id the
// caller supplied plus the reason the daemon returned. The result carries all
// three as fields instead of a prose error string.
func decodeControlRefusal(action, id string, err error) (core.WireControlResult, bool) {
	reason, ok := controlRefusalReason(err)
	if !ok {
		return core.WireControlResult{}, false
	}
	return core.NewWireControlRefusal(core.ParseControlAction(action), core.RunID(id), reason), true
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

// applyControlBatch applies one verb to each id and keeps the outcome per id.
// A no-op notice is success with a reason, never a failed id.
func applyControlBatch(ctx context.Context, sv Services, a ControlArgs) (any, error) {
	results := make([]controlItem, 0, len(a.RunIDs))
	for _, id := range a.RunIDs {
		item := controlItem{ID: id, Status: "ok"}
		if err := applyControl(ctx, sv, a.Action, id, a.PR, a.SHA, a.Message); err != nil {
			if reason, ok := noticeReason(err); ok {
				item.Reason = reason
			} else {
				item.Status = "error"
				item.Error = err.Error()
				if refusal, ok := decodeControlRefusal(a.Action, id, err); ok {
					item.Reason = refusal.Reason
				}
			}
		}
		results = append(results, item)
	}
	return controlBatch{Action: a.Action, Results: results}, nil
}

// batchFailed reads the outcome back from the wire form. A replay from the
// durable store returns raw JSON rather than a controlBatch value, so the
// check cannot type-assert the Go value.
func batchFailed(out any) bool {
	wire, err := json.Marshal(out)
	if err != nil {
		return false
	}
	var batch controlBatch
	return json.Unmarshal(wire, &batch) == nil && batch.hasError()
}

// controlMany journals the batch when the caller supplies an operation id. A
// replay returns the stored per-id results without re-applying any cancel,
// unblock or retry, and keeps IsError when an id failed.
func controlMany(ctx context.Context, journal *opJournal, sv Services, a ControlArgs) (*msdk.CallToolResult, any, error) {
	if len(a.RunIDs) == 0 {
		return nil, nil, core.Errorf(core.CodeInvalidInput, "run_ids must not be empty")
	}
	if !core.ParseControlAction(a.Action).Valid() {
		return nil, nil, core.Errorf(core.CodeInvalidInput, "unknown action %q", a.Action)
	}
	if err := requireService("control service", sv.Remote, sv.Control); err != nil {
		return nil, nil, err
	}
	out, err := journal.run(ctx, a.OperationID, func(ctx context.Context) (any, error) {
		return applyControlBatch(ctx, sv, a)
	})
	if err != nil {
		return nil, nil, err
	}
	result := jsonResult(out)
	if batchFailed(out) {
		result.IsError = true
	}
	return result, nil, nil
}
