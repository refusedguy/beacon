package mcp

import (
	"context"
	"os"
	"strings"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// DispatchArgs is the input schema for beacon_dispatch.
type DispatchArgs struct {
	N int `json:"n"`
}

// dispatchOutcome is one caller-facing line of a dispatch. Every candidate
// gets one, whether it started, was skipped or failed to start.
type dispatchOutcome struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Launch   string `json:"launch_source,omitempty"`
	Identity string `json:"agent_identity,omitempty"`
}

// dispatchResult is the wire shape of beacon_dispatch. The fields follow the
// alphabetical key order json.Marshal gives a map, so encoding the struct emits
// the same bytes without allocating a map or boxing four values into any. A
// second slice of run pointers is also gone: RecordLaunch mutates each run in
// place, so the dispatched slice is already the post-launch view.
type dispatchResult struct {
	Failures []app.DispatchFailure `json:"failures"`
	Runs     []*core.Run           `json:"runs"`
	Skipped  []app.SkippedTask     `json:"skipped"`
	Summary  []dispatchOutcome     `json:"summary"`
}

func registerDispatch(s *msdk.Server, sv Services) {
	msdk.AddTool(s, catalogTool("beacon_dispatch"), func(ctx context.Context, req *msdk.CallToolRequest, a DispatchArgs) (*msdk.CallToolResult, any, error) {
		if a.N <= 0 {
			return nil, nil, core.Errorf(core.CodeInvalidInput, "n must be positive")
		}
		if err := requireService("dispatcher", sv.Dispatcher); err != nil {
			return nil, nil, err
		}
		if parent := parentSession(req); parent != "" {
			ctx = app.WithParentSession(ctx, parent)
		}
		res, err := sv.Dispatcher.Dispatch(ctx, a.N)
		if err != nil {
			return nil, nil, err
		}
		source, agent := dispatchCaller(req)
		summary := make([]dispatchOutcome, 0, len(res.Runs)+len(res.Failures)+len(res.Skipped))
		for _, run := range res.Runs {
			run.RecordLaunch(source, agent)
			summary = append(summary, outcomeForRun(run))
		}
		for _, failure := range res.Failures {
			summary = append(summary, dispatchOutcome{
				ID: string(failure.TaskID), TaskID: string(failure.TaskID),
				State: "failed", Reason: failure.Err, Launch: string(source), Identity: agent,
			})
		}
		for _, skip := range res.Skipped {
			summary = append(summary, dispatchOutcome{
				ID: string(skip.TaskID), TaskID: string(skip.TaskID),
				State: "skipped", Reason: skip.Reason,
			})
		}
		return jsonResult(dispatchResult{
			Runs:     res.Runs,
			Skipped:  res.Skipped,
			Failures: res.Failures,
			Summary:  summary,
		}), nil, nil
	})
}

func outcomeForRun(run *core.Run) dispatchOutcome {
	reason := run.Reason
	if reason == "" {
		reason = string(run.State)
	}
	return dispatchOutcome{
		ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
		Reason: reason, Launch: string(run.LaunchSource), Identity: run.AgentIdentity,
	}
}

// normalizeLaunchSource maps caller metadata onto the neutral tokens core
// accepts. Vendor names stay at this boundary so core never learns a product's
// name. Unknown values pass through and resolve to daemon in core.
func normalizeLaunchSource(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "opencode":
		return string(core.LaunchMCP)
	case "tui":
		return string(core.LaunchUI)
	default:
		return raw
	}
}

// dispatchCaller reads the launch source and agent from MCP metadata. A call
// through this tool is an MCP launch unless a caller declares another source,
// as the CLI does when it forwards through the same transport.
func dispatchCaller(req *msdk.CallToolRequest) (core.LaunchSource, string) {
	source := core.LaunchMCP
	if raw := metaString(req, "beacon/launchSource", "beacon/launch_source", "ai.opencode/launchSource"); raw != "" {
		source = core.ParseLaunchSource(normalizeLaunchSource(raw))
	}
	return source, metaString(req, "beacon/agent", "ai.opencode/agent")
}

func metaString(req *msdk.CallToolRequest, keys ...string) string {
	if req == nil || req.Params == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := req.Params.Meta[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// parentSession reads the operator session from call metadata. A worker that
// inherits the pooled MCP daemon sends no meta, so fall back to the session the
// plugin placed in the server environment at startup. Per-call meta wins.
func parentSession(req *msdk.CallToolRequest) string {
	if meta := metaString(req,
		"ai.opencode/sessionID", "beacon/sessionID",
		"ai.opencode/sessionId", "beacon/sessionId",
		"opencode/sessionID", "sessionID", "sessionId",
	); meta != "" {
		return meta
	}
	return os.Getenv("BEACON_PARENT_SESSION")
}
