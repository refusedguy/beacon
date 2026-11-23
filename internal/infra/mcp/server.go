// Package mcp exposes the five thin controller tools to agents.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

type Services struct {
	Dispatcher *app.Dispatcher
	Control    *app.Control
	Status     app.StatusService
	Wait       app.WaitService
	Remote     func(ctx context.Context, action, runID string, pr int, sha string) error
}

type DispatchArgs struct {
	N int `json:"n"`
}

type WatchPRArgs struct {
	RunID string `json:"run_id"`
	PR    int    `json:"pr"`
}

type StatusArgs struct{}

type WaitArgs struct {
	RunID     string `json:"run_id"`
	TimeoutMS int    `json:"timeout_ms"`
}

type ControlArgs struct {
	Action string `json:"action"`
	RunID  string `json:"run_id"`
	SHA    string `json:"sha"`
	PR     int    `json:"pr"`
}

func NewServer(sv Services) *msdk.Server {
	s := msdk.NewServer(&msdk.Implementation{Name: "beacon", Version: "v1.0.0"}, nil)
	msdk.AddTool(s, &msdk.Tool{Name: "beacon_dispatch", Description: "Pick up to N non-overlapping tasks and start agents."}, func(ctx context.Context, req *msdk.CallToolRequest, a DispatchArgs) (*msdk.CallToolResult, any, error) {
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
		return jsonResult(map[string]any{
			"runs":     res.Runs,
			"skipped":  res.Skipped,
			"failures": res.Failures,
		}), nil, nil
	})
	msdk.AddTool(s, &msdk.Tool{Name: "beacon_watch_pr", Description: "Bind a pull request to a running task."}, func(ctx context.Context, _ *msdk.CallToolRequest, a WatchPRArgs) (*msdk.CallToolResult, any, error) {
		if sv.Remote != nil {
			if err := sv.Remote(ctx, "watch", a.RunID, a.PR, ""); err != nil {
				return nil, nil, err
			}
			return textResult("watching PR " + fmt.Sprint(a.PR)), nil, nil
		}
		if err := sv.Control.WatchPR(ctx, core.RunID(a.RunID), a.PR); err != nil {
			return nil, nil, err
		}
		return textResult("watching PR " + fmt.Sprint(a.PR)), nil, nil
	})
	msdk.AddTool(s, &msdk.Tool{Name: "beacon_status", Description: "List active runs, PRs and blockers."}, func(ctx context.Context, _ *msdk.CallToolRequest, _ StatusArgs) (*msdk.CallToolResult, any, error) {
		runs, err := sv.Status.Active(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(runs), nil, nil
	})
	msdk.AddTool(s, &msdk.Tool{Name: "beacon_wait", Description: "Wait for a run to reach an active verdict or a terminal state."}, func(ctx context.Context, _ *msdk.CallToolRequest, a WaitArgs) (*msdk.CallToolResult, any, error) {
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
	msdk.AddTool(s, &msdk.Tool{Name: "beacon_control", Description: "Control a run: approve, unblock, cancel, retry or bind a PR."}, func(ctx context.Context, _ *msdk.CallToolRequest, a ControlArgs) (*msdk.CallToolResult, any, error) {
		id := core.RunID(a.RunID)
		if sv.Remote != nil {
			if err := sv.Remote(ctx, a.Action, a.RunID, a.PR, a.SHA); err != nil {
				return nil, nil, err
			}
			return textResult("ok"), nil, nil
		}
		switch a.Action {
		case "approve":
			if err := sv.Control.Approve(ctx, id, a.SHA); err != nil {
				return nil, nil, err
			}
		case "unblock":
			if err := sv.Control.Unblock(ctx, id); err != nil {
				return nil, nil, err
			}
		case "cancel":
			if err := sv.Control.Cancel(ctx, id); err != nil {
				return nil, nil, err
			}
		case "retry":
			if err := sv.Control.Retry(ctx, id); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, core.Errorf(core.CodeInvalidInput, "unknown action %q", a.Action)
		}
		return textResult("ok"), nil, nil
	})
	return s
}

func parentSession(req *msdk.CallToolRequest) string {
	if req == nil || req.Params == nil {
		return ""
	}
	for _, key := range []string{"ai.opencode/sessionID", "beacon/sessionID"} {
		if v, ok := req.Params.Meta[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func textResult(s string) *msdk.CallToolResult {
	return &msdk.CallToolResult{Content: []msdk.Content{&msdk.TextContent{Text: s}}}
}

func jsonResult(v any) *msdk.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return textResult(fmt.Sprintf("{\"error\":%q}", err.Error()))
	}
	return textResult(string(b))
}

func ServeStdio(s *msdk.Server) error {
	sess, err := s.Connect(context.Background(), &msdk.StdioTransport{}, nil)
	if err != nil {
		return err
	}
	return sess.Wait()
}
