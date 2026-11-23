package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const maxLogReasonRunes = 160

// Decision is one structured decision. The same shape is written to sqlite
// and returned in the batched snapshot.
type Decision struct {
	ID       int64          `json:"id"`
	At       time.Time      `json:"at"`
	CycleID  string         `json:"cycle_id"`
	Kind     string         `json:"kind"`
	RunID    string         `json:"run_id"`
	TaskID   string         `json:"task_id"`
	PRNumber int            `json:"pr"`
	Verdict  string         `json:"verdict"`
	Action   string         `json:"action"`
	Reason   string         `json:"reason"`
	Fields   map[string]any `json:"fields,omitempty"`
}

// DecisionSink persists decisions emitted by the controller.
type DecisionSink interface {
	RecordDecision(ctx context.Context, d Decision) error
}

var decisionCapture = struct {
	sync.RWMutex
	sink DecisionSink
}{}

// SetDecisionSink installs the process-wide writer. A nil sink disables
// persistence without touching the stderr log.
func SetDecisionSink(s DecisionSink) {
	decisionCapture.Lock()
	decisionCapture.sink = s
	decisionCapture.Unlock()
}

// RecordDecision forwards a decision to the installed sink.
func RecordDecision(ctx context.Context, d Decision) {
	decisionCapture.RLock()
	sink := decisionCapture.sink
	decisionCapture.RUnlock()
	if sink == nil {
		return
	}
	if d.At.IsZero() {
		d.At = time.Now().UTC()
	}
	if err := sink.RecordDecision(ctx, d); err != nil {
		slog.WarnContext(ctx, "decision.persist_failed", "kind", d.Kind, "error", err)
	}
}

// shortLogReason keeps a log field scalar and bounded. Failure excerpts can be
// long, so only a short prefix reaches the log. The default handler redacts
// secrets from every string it sees.
func shortLogReason(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", " "))
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxLogReasonRunes {
		return s
	}
	return string(runes[:maxLogReasonRunes]) + "..."
}

func joinLogPaths(paths []core.ScopePath) string {
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		parts = append(parts, string(p))
	}
	return strings.Join(parts, ",")
}

// decisionAttrs adds the cycle correlation key to every decision record.
func decisionAttrs(ctx context.Context, fields ...any) []any {
	return append([]any{"cycle_id", cycleID(ctx)}, fields...)
}

func logDispatchCandidates(ctx context.Context, candidates, active, limit, budget int) {
	slog.InfoContext(ctx, "dispatch.candidates", decisionAttrs(ctx,
		"candidates", candidates,
		"active", active,
		"limit", limit,
		"budget", budget,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "dispatch.candidates",
		Fields: map[string]any{
			"candidates": candidates, "active": active, "limit": limit, "budget": budget,
		},
	})
}

func logDispatchSkip(ctx context.Context, s SkippedTask) {
	slog.InfoContext(ctx, "dispatch.skip", decisionAttrs(ctx,
		"task_id", string(s.TaskID),
		"code", string(s.Code),
		"blocked_by", string(s.BlockedBy),
		"blocked_by_run", string(s.BlockedByRun),
		"overlap", joinLogPaths(s.Overlap),
		"reason", shortLogReason(s.Reason),
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "dispatch.skip",
		TaskID: string(s.TaskID), RunID: string(s.BlockedByRun),
		Verdict: string(s.Code), Action: string(s.BlockedBy), Reason: s.Reason,
		Fields: map[string]any{
			"blocked_by": string(s.BlockedBy), "blocked_by_run": string(s.BlockedByRun),
			"overlap": joinLogPaths(s.Overlap),
		},
	})
}

// reconcileAction names the concrete step the reconciler takes for a verdict.
func reconcileAction(d core.MergeDecision) string {
	switch d.Verdict {
	case core.VerdictMerge:
		return "merge"
	case core.VerdictNeedsRebase:
		return "rebase"
	case core.VerdictNeedsAgentFix:
		return "wake"
	case core.VerdictBlocked:
		return "block"
	case core.VerdictNoop:
		return "noop"
	default:
		return "wait"
	}
}

func logReconcileDecision(ctx context.Context, pr int, run *core.Run, obs core.Observation, decision core.MergeDecision, action string) {
	slog.InfoContext(ctx, "reconcile.decision", decisionAttrs(ctx,
		"pr", pr,
		"run_id", string(run.ID),
		"task_id", string(run.TaskID),
		"head_sha", obs.HeadSHA,
		"observed_sha", run.ObservedSHA,
		"behind", obs.Behind,
		"state", string(run.State),
		"verdict", string(decision.Verdict),
		"reason", shortLogReason(decision.Reason),
		"action", action,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "reconcile.decision",
		RunID: string(run.ID), TaskID: string(run.TaskID), PRNumber: pr,
		Verdict: string(decision.Verdict), Action: action, Reason: decision.Reason,
		Fields: map[string]any{
			"head_sha": obs.HeadSHA, "observed_sha": run.ObservedSHA,
			"behind": obs.Behind, "state": string(run.State),
		},
	})
}

// logReconcileDecisionDeferred logs the final decision after Reconcile returns.
func logReconcileDecisionDeferred(ctx context.Context, pr int, run *core.Run, obs core.Observation, decision *core.MergeDecision) {
	if decision.Verdict != "" {
		logReconcileDecision(ctx, pr, run, obs, *decision, reconcileAction(*decision))
	}
}

func logTransitionSpawn(ctx context.Context, runID core.RunID, taskID core.TaskID, branch string) {
	slog.InfoContext(ctx, "transition.spawn", decisionAttrs(ctx,
		"run_id", string(runID),
		"task_id", string(taskID),
		"branch", branch,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.spawn",
		RunID: string(runID), TaskID: string(taskID),
		Fields: map[string]any{"branch": branch},
	})
}

func logTransitionWatchPR(ctx context.Context, runID core.RunID, pr int) {
	slog.InfoContext(ctx, "transition.watch_pr", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.watch_pr",
		RunID: string(runID), PRNumber: pr,
	})
}

func logTransitionRebase(ctx context.Context, runID core.RunID, pr, attempt int, newSHA, result string) {
	slog.InfoContext(ctx, "transition.rebase", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"attempt", attempt,
		"new_sha", newSHA,
		"result", result,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.rebase",
		RunID: string(runID), PRNumber: pr, Action: result,
		Fields: map[string]any{"attempt": attempt, "new_sha": newSHA},
	})
}

func logTransitionWake(ctx context.Context, runID core.RunID, pr int, result string) {
	slog.InfoContext(ctx, "transition.wake", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"result", result,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.wake",
		RunID: string(runID), PRNumber: pr, Action: result,
	})
}

func logTransitionMerge(ctx context.Context, runID core.RunID, pr int, result string) {
	slog.InfoContext(ctx, "transition.merge", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"result", result,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.merge",
		RunID: string(runID), PRNumber: pr, Action: result,
	})
}

func logTransitionDeliveryClaim(ctx context.Context, runID core.RunID, pr int, claimed bool) {
	slog.InfoContext(ctx, "transition.delivery_claim", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"claimed", claimed,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.delivery_claim",
		RunID: string(runID), PRNumber: pr,
		Fields: map[string]any{"claimed": claimed},
	})
}

func logTransitionParentNotify(ctx context.Context, runID core.RunID, pr int, result string) {
	slog.InfoContext(ctx, "transition.parent_notify", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"result", result,
	)...)
	RecordDecision(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.parent_notify",
		RunID: string(runID), PRNumber: pr, Action: result,
	})
}
