package mcp

import (
	"context"

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

var dispatchTool = &msdk.Tool{
	Name:        "beacon_dispatch",
	Description: "Pick up to N non-overlapping tasks and start agents.",
}

func registerDispatch(s *msdk.Server, sv Services) {
	msdk.AddTool(s, dispatchTool, func(ctx context.Context, req *msdk.CallToolRequest, a DispatchArgs) (*msdk.CallToolResult, any, error) {
		if sv.Dispatcher == nil {
			return nil, nil, core.Errorf(core.CodeUnavailable, "dispatcher is not configured")
		}
		if a.N <= 0 {
			a.N = 1
		}
		if parent := parentSession(req); parent != "" {
			ctx = app.WithParentSession(ctx, parent)
		}
		res, err := sv.Dispatcher.Dispatch(ctx, a.N)
		if err != nil {
			return nil, nil, err
		}
		source, agent := dispatchCaller(req)
		failed := failedRuns(ctx, sv.Dispatcher, res.Failures)
		summary := make([]dispatchOutcome, 0, len(res.Runs)+len(failed)+len(res.Skipped))
		runs := make([]*core.Run, 0, len(res.Runs)+len(failed))
		for _, run := range res.Runs {
			run.RecordLaunch(source, agent)
			runs = append(runs, run)
			summary = append(summary, outcomeForRun(run))
		}
		for _, failure := range res.Failures {
			run := failed[failure.TaskID]
			if run == nil {
				summary = append(summary, dispatchOutcome{
					ID: string(failure.TaskID), TaskID: string(failure.TaskID),
					State: "failed", Reason: failure.Err, Launch: string(source), Identity: agent,
				})
				continue
			}
			run.RecordLaunch(source, agent)
			runs = append(runs, run)
			summary = append(summary, outcomeForRun(run))
		}
		for _, skip := range res.Skipped {
			summary = append(summary, dispatchOutcome{
				ID: string(skip.TaskID), TaskID: string(skip.TaskID),
				State: "skipped", Reason: skip.Reason,
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
	reason := run.Reason
	if reason == "" {
		reason = string(run.State)
	}
	return dispatchOutcome{
		ID: string(run.ID), TaskID: string(run.TaskID), State: string(run.State),
		Reason: reason, Launch: string(run.LaunchSource), Identity: run.AgentIdentity,
	}
}

// dispatchCaller reads the launch source and agent from MCP metadata. A call
// through this tool is an MCP launch unless a caller declares another source,
// as the CLI does when it forwards through the same transport.
func dispatchCaller(req *msdk.CallToolRequest) (core.LaunchSource, string) {
	source := core.LaunchMCP
	if raw := metaString(req, "beacon/launchSource", "beacon/launch_source", "ai.opencode/launchSource"); raw != "" {
		source = core.ParseLaunchSource(raw)
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

// failedRuns recovers the persisted records for runs that failed to start.
// Dispatch reports those only as task IDs, so the caller-facing summary looks
// them up by task to give back a real run ID, state and reason.
func failedRuns(ctx context.Context, d *app.Dispatcher, failures []app.DispatchFailure) map[core.TaskID]*core.Run {
	out := map[core.TaskID]*core.Run{}
	if d == nil || d.Store == nil || len(failures) == 0 {
		return out
	}
	limit := len(failures) + app.DefaultRecentLimit
	recent, err := d.Store.ListRecent(ctx, limit)
	if err != nil {
		return out
	}
	for _, failure := range failures {
		for _, run := range recent {
			if run.TaskID == failure.TaskID {
				out[failure.TaskID] = run
				break
			}
		}
	}
	return out
}

func parentSession(req *msdk.CallToolRequest) string {
	return metaString(req, "ai.opencode/sessionID", "beacon/sessionID")
}
