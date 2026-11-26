package app

import (
	"context"
	"fmt"
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

// DecisionLookup is an optional read side of a sink. A sink that implements it
// lets repeated reconcile decisions be dropped instead of filling history.
type DecisionLookup interface {
	LastDecision(ctx context.Context, runID string) (*Decision, error)
}

// DecisionLogger writes decisions to an injected sink. A nil sink disables
// persistence without touching the stderr log. The reconcile cache lives
// behind a pointer so Record can create and retain it across calls.
type DecisionLogger struct {
	Sink  DecisionSink
	cache *decisionCache
}

// decisionCache holds the last written reconcile signature for each run. A
// hit lets Record skip both the store read and the write when an observation
// has not changed.
type decisionCache struct {
	mu   sync.Mutex
	sigs map[string]string
}

func (c *decisionCache) get(runID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sig, ok := c.sigs[runID]
	return sig, ok
}

func (c *decisionCache) set(runID, sig string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sigs == nil {
		c.sigs = make(map[string]string)
	}
	c.sigs[runID] = sig
}

// decisionCacheInitMu guards the lazy creation of cache on a DecisionLogger.
var decisionCacheInitMu sync.Mutex

func (l *DecisionLogger) cacheFor() *decisionCache {
	decisionCacheInitMu.Lock()
	defer decisionCacheInitMu.Unlock()
	if l.cache == nil {
		l.cache = &decisionCache{}
	}
	return l.cache
}

// Record forwards a decision to the logger's sink.
func (l *DecisionLogger) Record(ctx context.Context, d Decision) {
	sink := l.Sink
	if sink == nil {
		return
	}
	reconcile := d.Kind == "reconcile.decision" && d.RunID != ""
	var sig string
	if reconcile {
		sig = decisionSignature(d)
		cache := l.cacheFor()
		if cached, ok := cache.get(d.RunID); ok && cached == sig {
			slog.DebugContext(ctx, "decision.duplicate_skipped", "kind", d.Kind, "run_id", d.RunID)
			return
		}
		if duplicateDecision(ctx, sink, d, sig) {
			cache.set(d.RunID, sig)
			slog.DebugContext(ctx, "decision.duplicate_skipped", "kind", d.Kind, "run_id", d.RunID)
			return
		}
	}
	if d.At.IsZero() {
		d.At = time.Now().UTC()
	}
	if err := sink.RecordDecision(ctx, d); err != nil {
		slog.WarnContext(ctx, "decision.persist_failed", "kind", d.Kind, "error", err)
		return
	}
	if reconcile {
		l.cache.set(d.RunID, sig)
	}
}

// duplicateDecision compares a decision with the newest stored one for the
// same run. Only reconcile decisions are deduplicated; every other kind is a
// distinct event in a run's timeline. sig is the caller's signature of d, so d
// is not signed a second time.
func duplicateDecision(ctx context.Context, sink DecisionSink, d Decision, sig string) bool {
	lookup, ok := sink.(DecisionLookup)
	if !ok {
		return false
	}
	last, err := lookup.LastDecision(ctx, d.RunID)
	if err != nil {
		slog.WarnContext(ctx, "decision.last_lookup_failed", "run_id", d.RunID, "error", err)
		return false
	}
	return last != nil && decisionSignature(*last) == sig
}

// decisionSignature is the part of a reconcile decision that describes a real
// change. Cycle IDs and timestamps are ignored so repeated polls stay equal.
func decisionSignature(d Decision) string {
	return strings.Join([]string{
		d.Kind, d.Verdict, d.Action, d.Reason,
		fmt.Sprint(d.Fields["head_sha"]), fmt.Sprint(d.Fields["state"]),
	}, "\x00")
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

func (l *DecisionLogger) logDispatchCandidates(ctx context.Context, candidates, active, limit, budget int) {
	slog.InfoContext(ctx, "dispatch.candidates", decisionAttrs(ctx,
		"candidates", candidates,
		"active", active,
		"limit", limit,
		"budget", budget,
	)...)
	l.Record(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "dispatch.candidates",
		Fields: map[string]any{
			"candidates": candidates, "active": active, "limit": limit, "budget": budget,
		},
	})
}

func (l *DecisionLogger) logDispatchSkip(ctx context.Context, s SkippedTask) {
	slog.InfoContext(ctx, "dispatch.skip", decisionAttrs(ctx,
		"task_id", string(s.TaskID),
		"code", string(s.Code),
		"blocked_by", string(s.BlockedBy),
		"blocked_by_run", string(s.BlockedByRun),
		"overlap", joinLogPaths(s.Overlap),
		"reason", shortLogReason(s.Reason),
	)...)
	l.Record(ctx, Decision{
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

func (l *DecisionLogger) logReconcileDecision(ctx context.Context, pr int, run *core.Run, obs core.Observation, decision core.MergeDecision, action string) {
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
	l.Record(ctx, Decision{
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
func (l *DecisionLogger) logReconcileDecisionDeferred(ctx context.Context, pr int, run *core.Run, obs core.Observation, decision *core.MergeDecision) {
	if decision.Verdict != "" {
		l.logReconcileDecision(ctx, pr, run, obs, *decision, reconcileAction(*decision))
	}
}

func (l *DecisionLogger) logTransitionSpawn(ctx context.Context, runID core.RunID, taskID core.TaskID, branch string) {
	slog.InfoContext(ctx, "transition.spawn", decisionAttrs(ctx,
		"run_id", string(runID),
		"task_id", string(taskID),
		"branch", branch,
	)...)
	l.Record(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.spawn",
		RunID: string(runID), TaskID: string(taskID),
		Fields: map[string]any{"branch": branch},
	})
}

// emitTransition writes the shared slog record and Decision for a run
// transition. An empty result leaves out the result attribute and Action field,
// which keeps watch_pr's decision shape unchanged.
func (l *DecisionLogger) emitTransition(ctx context.Context, kind string, runID core.RunID, pr int, result string) {
	attrs := decisionAttrs(ctx, "run_id", string(runID), "pr", pr)
	d := Decision{
		CycleID: cycleID(ctx), Kind: kind,
		RunID: string(runID), PRNumber: pr,
	}
	if result != "" {
		attrs = append(attrs, "result", result)
		d.Action = result
	}
	slog.InfoContext(ctx, kind, attrs...)
	l.Record(ctx, d)
}

func (l *DecisionLogger) logTransitionWatchPR(ctx context.Context, runID core.RunID, pr int) {
	l.emitTransition(ctx, "transition.watch_pr", runID, pr, "")
}

func (l *DecisionLogger) logTransitionRebase(ctx context.Context, runID core.RunID, pr, attempt int, newSHA, result string) {
	slog.InfoContext(ctx, "transition.rebase", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"attempt", attempt,
		"new_sha", newSHA,
		"result", result,
	)...)
	l.Record(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.rebase",
		RunID: string(runID), PRNumber: pr, Action: result,
		Fields: map[string]any{"attempt": attempt, "new_sha": newSHA},
	})
}

func (l *DecisionLogger) logTransitionWake(ctx context.Context, runID core.RunID, pr int, result string) {
	l.emitTransition(ctx, "transition.wake", runID, pr, result)
}

func (l *DecisionLogger) logTransitionMerge(ctx context.Context, runID core.RunID, pr int, result string) {
	l.emitTransition(ctx, "transition.merge", runID, pr, result)
}

func (l *DecisionLogger) logTransitionDeliveryClaim(ctx context.Context, runID core.RunID, pr int, claimed bool) {
	slog.InfoContext(ctx, "transition.delivery_claim", decisionAttrs(ctx,
		"run_id", string(runID),
		"pr", pr,
		"claimed", claimed,
	)...)
	l.Record(ctx, Decision{
		CycleID: cycleID(ctx), Kind: "transition.delivery_claim",
		RunID: string(runID), PRNumber: pr,
		Fields: map[string]any{"claimed": claimed},
	})
}

func (l *DecisionLogger) logTransitionParentNotify(ctx context.Context, runID core.RunID, pr int, result string) {
	l.emitTransition(ctx, "transition.parent_notify", runID, pr, result)
}
