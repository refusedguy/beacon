package app

import (
	"context"
	"log/slog"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

const maxLogReasonRunes = 160

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
}

func logTransitionWatchPR(ctx context.Context, runID core.RunID, pr int) {
	slog.InfoContext(ctx, "transition.watch_pr", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
	)...)
}

func logTransitionRebase(ctx context.Context, runID core.RunID, pr, attempt int, newSHA, result string) {
	slog.InfoContext(ctx, "transition.rebase", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"attempt", attempt,
		"new_sha", newSHA,
		"result", result,
	)...)
}

func logTransitionWake(ctx context.Context, runID core.RunID, pr int, result string) {
	slog.InfoContext(ctx, "transition.wake", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"result", result,
	)...)
}

func logTransitionMerge(ctx context.Context, runID core.RunID, pr int, result string) {
	slog.InfoContext(ctx, "transition.merge", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"result", result,
	)...)
}

func logTransitionDeliveryClaim(ctx context.Context, runID core.RunID, pr int, claimed bool) {
	slog.InfoContext(ctx, "transition.delivery_claim", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"claimed", claimed,
	)...)
}

func logTransitionParentNotify(ctx context.Context, runID core.RunID, pr int, result string) {
	slog.InfoContext(ctx, "transition.parent_notify", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"result", result,
	)...)
}
