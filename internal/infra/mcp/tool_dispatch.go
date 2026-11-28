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
// gets one, whether it started, was skipped or failed to start. Reason is the
// reason the run or skip actually gave; an empty reason stays empty. Next tells
// the caller how to clear a skip or a failed start.
type dispatchOutcome struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Next     string `json:"next"`
	Launch   string `json:"launch_source,omitempty"`
	Identity string `json:"agent_identity,omitempty"`
}

// dispatchFailureNext is the step that clears a worker that failed to start.
// The outcome's Reason carries the underlying error.
const dispatchFailureNext = "inspect the failure reason, resolve it, then dispatch again"

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
		runs := make([]*core.Run, 0, len(res.Runs))
		for _, run := range res.Runs {
			run.RecordLaunch(source, agent)
			runs = append(runs, run)
			summary = append(summary, outcomeForRun(run))
		}
		for _, failure := range res.Failures {
			summary = append(summary, dispatchOutcome{
				ID: string(failure.TaskID), TaskID: string(failure.TaskID),
				State: "failed", Reason: failure.Err, Next: dispatchFailureNext,
				Launch: string(source), Identity: agent,
			})
		}
		for _, skip := range res.Skipped {
			summary = append(summary, dispatchOutcome{
				ID: string(skip.TaskID), TaskID: string(skip.TaskID),
				State: "skipped", Reason: skip.Reason, Next: dispatchSkipNext(skip),
			})
		}
		return jsonResult(map[string]any{
			"runs":     runs,
			"skipped":  res.Skipped,
			"failures": res.Failures,
			"summary":  summary,
		}), nil, nil
	})
}

func outcomeForRun(run *core.Run) dispatchOutcome {
	return dispatchOutcome{
		ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
		Reason: run.Reason, Launch: string(run.LaunchSource), Identity: run.AgentIdentity,
	}
}

// dispatchSkipNext returns the action a skip tells the caller to take. A skip
// built without a Next still gets its action filled when the raw skipped array
// is encoded, so the summary mirrors that default instead of going empty.
func dispatchSkipNext(skip app.SkippedTask) string {
	if skip.Next != "" {
		return skip.Next
	}
	return "review the skip reason, then dispatch again"
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
