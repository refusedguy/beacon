package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
// persistence without touching the stderr log. The reconcile cache and the
// deferred dispatch queue live behind pointers so a logger can create and
// retain them across calls.
type DecisionLogger struct {
	Sink     DecisionSink
	cache    *decisionCache
	deferred *deferredDecisions
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

// decisionStateInitMu guards the lazy creation of cache and deferred on a
// DecisionLogger. Both live behind pointers so a logger retains them across
// calls on its value copy.
var decisionStateInitMu sync.Mutex

func (l *DecisionLogger) cacheFor() *decisionCache {
	decisionStateInitMu.Lock()
	defer decisionStateInitMu.Unlock()
	if l.cache == nil {
		l.cache = &decisionCache{}
	}
	return l.cache
}

func (l *DecisionLogger) deferredFor() *deferredDecisions {
	decisionStateInitMu.Lock()
	defer decisionStateInitMu.Unlock()
	if l.deferred == nil {
		l.deferred = &deferredDecisions{}
	}
	return l.deferred
}

// deferredDecision is one audit row held until the caller has built its
// response. The context keeps the cycle ID and other values while dropping
// cancellation, so a cancelled tool call cannot discard the record.
type deferredDecision struct {
	ctx   context.Context
	d     Decision
	write func(context.Context, Decision)
}

// deferredDecisions buffers dispatch audit rows and writes them after the
// caller returns. One goroutine drains one batch in emission order, which
// keeps the rows for a run ordered. Wait drains every queued batch so a
// shutdown can persist all records before exit.
type deferredDecisions struct {
	mu      sync.Mutex
	pending []deferredDecision
	wg      sync.WaitGroup
}

func (q *deferredDecisions) append(item deferredDecision) {
	q.mu.Lock()
	q.pending = append(q.pending, item)
	q.wg.Add(1)
	q.mu.Unlock()
}

func (q *deferredDecisions) flush() {
	q.mu.Lock()
	batch := q.pending
	q.pending = nil
	q.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	go func() {
		for _, item := range batch {
			item.write(item.ctx, item.d)
			q.wg.Done()
		}
	}()
}

func (q *deferredDecisions) Wait() {
	q.wg.Wait()
}

// Record forwards a decision to the logger's sink without delay.
func (l *DecisionLogger) Record(ctx context.Context, d Decision) {
	l.recordNow(ctx, d)
}

// deferRecord queues an audit row for a write after the caller has built its
// response. Sink IO never happens on the caller's path, and the detached
// context keeps the row even when the caller is cancelled.
func (l *DecisionLogger) deferRecord(ctx context.Context, d Decision) {
	if l.Sink == nil {
		return
	}
	l.deferredFor().append(deferredDecision{
		ctx:   context.WithoutCancel(ctx),
		d:     d,
		write: l.recordNow,
	})
}

// flushDeferred starts the queued writes and returns immediately. A caller
// invokes it once its response is built, so a slow sink cannot hold up the
// worker launch path.
func (l *DecisionLogger) flushDeferred() {
	decisionStateInitMu.Lock()
	q := l.deferred
	decisionStateInitMu.Unlock()
	if q != nil {
		q.flush()
	}
}

// Wait flushes any queued rows and blocks until every deferred row for this
// logger reaches the sink. A shutdown calls it to persist the full audit trail.
func (l *DecisionLogger) Wait() {
	decisionStateInitMu.Lock()
	q := l.deferred
	decisionStateInitMu.Unlock()
	if q != nil {
		q.flush()
		q.Wait()
	}
}

// recordNow is the shared sink write behind Record and the deferred flush.
func (l *DecisionLogger) recordNow(ctx context.Context, d Decision) {
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

// shortLogReason keeps a log field scalar and bounded. A byte length check
// skips the rune conversion for short reasons, and longer valid UTF-8 is
// walked to the cap. Invalid UTF-8 keeps the eager conversion so the output
// stays byte identical. The default handler redacts every string it sees.
func shortLogReason(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", " "))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLogReasonRunes {
		return s
	}
	if !utf8.ValidString(s) {
		r := []rune(s)
		if len(r) <= maxLogReasonRunes {
			return s
		}
		return string(r[:maxLogReasonRunes]) + "..."
	}
	i, n := 0, 0
	for i < len(s) && n < maxLogReasonRunes {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	if i == len(s) {
		return s
	}
	return s[:i] + "..."
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
	l.deferRecord(ctx, Decision{
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
	l.deferRecord(ctx, Decision{
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
	return core.ActionForVerdict(d.Verdict).Label
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

// transitionExtras carries the attributes and Decision fields that one
// transition adds on top of the shared run_id, pr and result core.
type transitionExtras struct {
	taskID core.TaskID
	attrs  []any
	fields map[string]any
}

// decisionWrite is the sink path a transition uses. Dispatch transitions queue
// their rows; every other transition writes them before returning.
type decisionWrite func(context.Context, Decision)

// emitTransition writes the shared slog record and Decision for a run
// transition. An empty result leaves out the result attribute and Action field,
// which keeps watch_pr's decision shape unchanged. A zero PR leaves out the pr
// attribute, which keeps spawn's log shape unchanged.
func (l *DecisionLogger) emitTransition(ctx context.Context, kind string, runID core.RunID, pr int, result string, extra transitionExtras) {
	l.emitTransitionTo(ctx, kind, runID, pr, result, extra, l.Record)
}

// emitTransitionDeferred logs a dispatch transition now and queues its Decision
// until the caller has built its response.
func (l *DecisionLogger) emitTransitionDeferred(ctx context.Context, kind string, runID core.RunID, pr int, result string, extra transitionExtras) {
	l.emitTransitionTo(ctx, kind, runID, pr, result, extra, l.deferRecord)
}

func (l *DecisionLogger) emitTransitionTo(ctx context.Context, kind string, runID core.RunID, pr int, result string, extra transitionExtras, write decisionWrite) {
	attrs := decisionAttrs(ctx, "run_id", string(runID))
	if pr != 0 {
		attrs = append(attrs, "pr", pr)
	}
	attrs = append(attrs, extra.attrs...)
	if result != "" {
		attrs = append(attrs, "result", result)
	}
	d := Decision{
		CycleID: cycleID(ctx), Kind: kind,
		RunID: string(runID), TaskID: string(extra.taskID), PRNumber: pr,
		Action: result, Fields: extra.fields,
	}
	slog.InfoContext(ctx, kind, attrs...)
	write(ctx, d)
}

func (l *DecisionLogger) logTransitionSpawn(ctx context.Context, runID core.RunID, taskID core.TaskID, branch string) {
	l.emitTransitionDeferred(ctx, "transition.spawn", runID, 0, "", transitionExtras{
		taskID: taskID,
		attrs:  []any{"task_id", string(taskID), "branch", branch},
		fields: map[string]any{"branch": branch},
	})
}

func (l *DecisionLogger) logTransitionWatchPR(ctx context.Context, runID core.RunID, pr int) {
	l.emitTransitionDeferred(ctx, "transition.watch_pr", runID, pr, "", transitionExtras{})
}

func (l *DecisionLogger) logTransitionRebase(ctx context.Context, runID core.RunID, pr, attempt int, newSHA, result string) {
	l.emitTransition(ctx, "transition.rebase", runID, pr, result, transitionExtras{
		attrs:  []any{"attempt", attempt, "new_sha", newSHA},
		fields: map[string]any{"attempt": attempt, "new_sha": newSHA},
	})
}

func (l *DecisionLogger) logTransitionWake(ctx context.Context, runID core.RunID, pr int, result string) {
	l.emitTransition(ctx, "transition.wake", runID, pr, result, transitionExtras{})
}

func (l *DecisionLogger) logTransitionMerge(ctx context.Context, runID core.RunID, pr int, result string) {
	l.emitTransition(ctx, "transition.merge", runID, pr, result, transitionExtras{})
}

func (l *DecisionLogger) logTransitionDeliveryClaim(ctx context.Context, runID core.RunID, pr int, claimed bool) {
	l.emitTransition(ctx, "transition.delivery_claim", runID, pr, "", transitionExtras{
		attrs:  []any{"claimed", claimed},
		fields: map[string]any{"claimed": claimed},
	})
}

func (l *DecisionLogger) logTransitionParentNotify(ctx context.Context, runID core.RunID, pr int, result string) {
	l.emitTransition(ctx, "transition.parent_notify", runID, pr, result, transitionExtras{})
}
