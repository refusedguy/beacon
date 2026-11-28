package mcp

import (
	"context"
	"errors"
	"time"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// WaitArgs is the input schema for beacon_wait.
type WaitArgs struct {
	RunID     string `json:"run_id"`
	TimeoutMS int    `json:"timeout_ms"`
}

// maxWaitMS bounds a wait so a caller cannot hold an agent open indefinitely.
const maxWaitMS = 600000

// waitTimeoutResult is the answer when the deadline fires before the run
// reaches a verdict. It carries the last observed state so the caller can pick
// its next action without a separate status round trip.
type waitTimeoutResult struct {
	RunID     string `json:"run_id"`
	LastState string `json:"last_state"`
	Reason    string `json:"reason"`
	TimedOut  bool   `json:"timed_out"`
	Next      string `json:"next"`
}

// lastRunStore remembers the last run a wait read. WaitFor drops that run when
// the deadline fires, so the tool keeps it at the store boundary instead of
// re-reading through an already expired context.
type lastRunStore struct {
	inner app.WaitStore
	last  *core.Run
}

func (s *lastRunStore) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	run, err := s.inner.Get(ctx, id)
	if err == nil && run != nil {
		s.last = run
	}
	return run, err
}

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
		recorder := &lastRunStore{inner: sv.Wait.Store}
		wait := sv.Wait
		wait.Store = recorder
		run, err := wait.WaitFor(cctx, core.RunID(a.RunID))
		if err == nil {
			return jsonResult(run), nil, nil
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return jsonResult(waitTimeoutResult{
			RunID:     a.RunID,
			LastState: lastRunState(recorder.last),
			Reason:    lastRunReason(recorder.last),
			TimedOut:  true,
			Next:      nextWaitAction(recorder.last),
		}), nil, nil
	})
}

func lastRunState(run *core.Run) string {
	if run == nil {
		return ""
	}
	return string(run.State)
}

func lastRunReason(run *core.Run) string {
	if run == nil {
		return ""
	}
	return run.Reason
}

// nextWaitAction maps the last observed state onto the step that clears it.
// The instructions follow the state-to-action table in GLOSSARY.md.
func nextWaitAction(run *core.Run) string {
	if run == nil {
		return "inspect beacon_status, then call beacon_wait again"
	}
	switch run.State {
	case core.RunPending:
		return "wait for the worker to start, then call beacon_wait again"
	case core.RunRunning:
		return "bind the PR with beacon_watch_pr, then call beacon_wait again"
	case core.RunWaitingCI:
		return "wait for checks, then call beacon_wait again"
	case core.RunNeedsRebase:
		return "let the reconciler rebase the branch, then call beacon_wait again"
	case core.RunNeedsAgentFix:
		return "let the fixer session finish, then call beacon_wait again"
	case core.RunBlocked:
		return "resolve the incident, then use beacon_control {\"action\":\"unblock\"}"
	case core.RunMerged:
		return "no action, the run is merged"
	case core.RunClosed:
		return "inspect reason, then use beacon_control {\"action\":\"retry\"}"
	default:
		return "inspect beacon_status, then call beacon_wait again"
	}
}
