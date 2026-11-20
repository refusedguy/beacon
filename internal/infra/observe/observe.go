// Package observe is the single logging and counting seam of Beacon. It has no
// dependency on domain or adapter packages, so any layer can report a fact
// without an import cycle. Every record is a structured slog event with an
// event field; counters and histograms are process-local and exported by
// `beacon metrics`.
package observe

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is the standard library log level.
type Level = slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// ParseLevel maps a configuration word onto a log level. An empty value means
// info. The boolean reports whether the word was recognised.
func ParseLevel(raw string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return LevelDebug, true
	case "", "info":
		return LevelInfo, true
	case "warn":
		return LevelWarn, true
	case "error":
		return LevelError, true
	default:
		return LevelInfo, false
	}
}

// Field names of one event. Keeping the keys here avoids typos in what an
// operator greps for.
const (
	FieldEvent     = "event"
	FieldWatchID   = "watchID"
	FieldPR        = "pr"
	FieldVerdict   = "verdict"
	FieldAdapter   = "adapter"
	FieldDuration  = "duration"
	FieldError     = "error"
	FieldRevision  = "revision"
	FieldActor     = "actor"
	FieldReason    = "reason"
	FieldStatus    = "status"
	FieldCommand   = "command"
	FieldWatches   = "watches"
	FieldReady     = "ready"
	FieldRetry     = "retry"
	FieldMerges    = "merges"
	FieldWakes     = "wakes"
	FieldRefs      = "refs"
	FieldCount     = "count"
	FieldUpdated   = "updated"
	FieldSkipped   = "skipped"
	FieldCycleID   = "cycleID"
	FieldBlockerID = "blockerID"
	FieldPoints    = "points"
)

// Event names of the blocker and auto-merge transitions reported by the
// orchestrator. Keeping the literals here lets callers log a fact without
// agreeing on a string in every package.
const (
	BlockerOpened   = "blocker_opened"
	BlockerResolved = "blocker_resolved"
	OrphanEscalated = "orphan_escalated"
	AutoMergeHeld   = "auto_merge_held"
)

// Fields are the structured values of one event.
type Fields map[string]any

// Counter names exported by Metrics. The _total suffix is the Prometheus
// convention for counters and is part of the exported name.
const (
	CounterPollCycles       = "poll_cycles_total"
	CounterVerdictPublished = "verdict_published_total"
	CounterMergeAttempts    = "merge_attempts_total"
	CounterMergeSuccess     = "merge_success_total"
	CounterMergeFailures    = "merge_failures_total"
	CounterWakeDelivered    = "wake_delivered_total"
	CounterWakeErrors       = "wake_errors_total"
	CounterWakeSkipped      = "wake_skipped_total"
	CounterRebaseRequests   = "rebase_requests_total"
	CounterDependencyWaits  = "dependency_waits_total"
	CounterBlocks           = "blocks_total"
	CounterActorErrors      = "actor_errors_total"
)

// Older names kept as aliases so call sites written against the first version
// of the package continue to count the same thing.
const (
	CounterMergeAttempt    = CounterMergeAttempts
	CounterWakeFailed      = CounterWakeErrors
	CounterRebaseRequested = CounterRebaseRequests
)

// Histogram names for the durations an operator cares about.
const (
	HistogramPollCycle = "poll_cycle_duration_seconds"
	HistogramMerge     = "merge_duration_seconds"
	HistogramWake      = "wake_duration_seconds"
	HistogramRun       = "run_duration_seconds"
)

// Label keys of the labelled counters.
const (
	LabelVerdict = "verdict"
	LabelAdapter = "adapter"
)

// Sink is the thin port the Beacon cycle calls. Log writes one structured
// record through slog, Inc adds one to a named counter. The global default is
// a no-op until the composition root installs a Recorder, so a call site never
// needs a nil check and existing tests keep working.
type Sink interface {
	Log(level Level, event string, fields Fields)
	Inc(name, labelValue string)
}

// histogramSink is optional: a Recorder implements it, a no-op sink does not.
type histogramSink interface {
	observeHistogram(name string, seconds float64)
}

var (
	defaultMu   sync.RWMutex
	defaultSink Sink = nopSink{}
)

// Default returns the installed sink, or a no-op sink before wiring runs.
func Default() Sink {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultSink
}

// SetDefault installs s as the process-wide sink. A nil argument restores the
// no-op sink, which is what tests use to isolate themselves.
func SetDefault(s Sink) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if s == nil {
		s = nopSink{}
	}
	defaultSink = s
}

// nopSink drops every record and count.
type nopSink struct{}

func (nopSink) Log(Level, string, Fields)        {}
func (nopSink) Inc(string, string)               {}
func (nopSink) observeHistogram(string, float64) {}

// Recorder combines a slog logger and a metric set into one Sink. A nil field
// is safe: the recorder simply skips that half.
type Recorder struct {
	log     *slog.Logger
	metrics *Metrics
}

// NewRecorder builds a Recorder from a slog logger and a metric set.
func NewRecorder(log *slog.Logger, metrics *Metrics) *Recorder {
	return &Recorder{log: log, metrics: metrics}
}

// Logger returns the recorder's slog logger. It may be nil.
func (r *Recorder) Logger() *slog.Logger {
	if r == nil {
		return nil
	}
	return r.log
}

// Metrics returns the recorder's counters. It may be nil.
func (r *Recorder) Metrics() *Metrics {
	if r == nil {
		return nil
	}
	return r.metrics
}

// Log writes one structured record through slog. The message stays empty so
// the event attribute carries the meaning in both text and JSON output.
func (r *Recorder) Log(level Level, event string, fields Fields) {
	if r == nil || r.log == nil {
		return
	}
	r.log.Log(context.Background(), level, "", eventAttrs(event, fields)...)
}

// Inc adds one to a counter through the recorder's metric set.
func (r *Recorder) Inc(name, labelValue string) {
	if r == nil || r.metrics == nil {
		return
	}
	r.metrics.Inc(name, labelValue)
}

func (r *Recorder) observeHistogram(name string, seconds float64) {
	if r == nil || r.metrics == nil {
		return
	}
	r.metrics.Observe(name, seconds)
}

// eventAttrs flattens a field set into slog attributes in sorted key order.
// Sorting makes text output stable enough to read and to assert in tests.
func eventAttrs(event string, fields Fields) []any {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]any, 0, len(keys)*2+2)
	attrs = append(attrs, FieldEvent, event)
	for _, k := range keys {
		attrs = append(attrs, k, fields[k])
	}
	return attrs
}

// MetricsOf returns the metric set behind s when s is a *Recorder. It returns
// nil for the no-op default or any other sink.
func MetricsOf(s Sink) *Metrics {
	if r, ok := s.(*Recorder); ok {
		return r.Metrics()
	}
	return nil
}

// Event writes one structured record through the installed sink. Call sites
// use it for transitions that have no dedicated helper.
func Event(level Level, event string, fields Fields) {
	Default().Log(level, event, fields)
}

// LogEvent is the older spelling of Event, kept for existing call sites.
func LogEvent(event string, level Level, fields Fields) { Event(level, event, fields) }

// Count adds one to a named counter through the installed sink.
func Count(name, labelValue string) { Default().Inc(name, labelValue) }

// Observe records one duration in seconds in a histogram.
func Observe(name string, seconds float64) {
	if s, ok := Default().(histogramSink); ok {
		s.observeHistogram(name, seconds)
	}
}

// cycleTracker accumulates the numbers of one daemon pass. The daemon calls
// DaemonPass at the start of a pass and the CLI calls FlushCycle on exit, so
// the summary is emitted without the loop needing to know about counters.
type cycleTracker struct {
	mu                                   sync.Mutex
	start                                time.Time
	started                              bool
	watches, ready, retry, merges, wakes int
}

var cycles = &cycleTracker{start: time.Now()}

func (c *cycleTracker) note(fn func(*cycleTracker)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

func (c *cycleTracker) flush(now time.Time) (CycleStats, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := CycleStats{
		Watches: c.watches, Ready: c.ready, Retry: c.retry,
		Merges: c.merges, Wakes: c.wakes, Duration: now.Sub(c.start),
	}
	started := c.started
	c.watches, c.ready, c.retry, c.merges, c.wakes = 0, 0, 0, 0, 0
	c.started = false
	c.start = now
	return s, started
}

// PollCycle records the start of one CI poll and its number of Watches.
func PollCycle() { Default().Inc(CounterPollCycles, "") }

// PollCycleStart logs the beginning of a poll. The counter is incremented by
// PollCycle so that a caller which only counts still gets a correct total.
func PollCycleStart(watches int) {
	Default().Inc(CounterPollCycles, "")
	cycles.note(func(c *cycleTracker) { c.watches, c.started = watches, true })
	Event(LevelDebug, "poll_cycle_start", Fields{FieldWatches: watches})
}

// PollCycleEnd logs the end of a poll: how many Watches were updated or
// skipped, and how long the cycle took.
func PollCycleEnd(updated, skipped int, d time.Duration) {
	Event(LevelDebug, "poll_cycle_end", Fields{
		FieldUpdated: updated, FieldSkipped: skipped, FieldDuration: d.String(),
	})
	Observe(HistogramPollCycle, d.Seconds())
}

// PollCycleDuration logs how long one CI poll took. It does not change the
// counter; PollCycle already did that.
func PollCycleDuration(d time.Duration) {
	Event(LevelDebug, "poll_cycle", Fields{FieldDuration: d.String()})
	Observe(HistogramPollCycle, d.Seconds())
}

// CycleStats is the compact summary of one daemon pass.
type CycleStats struct {
	Watches, Ready, Retry, Merges, Wakes int
	Duration                             time.Duration
}

// Cycle logs one INFO line per daemon cycle. The fields are the numbers an
// operator needs to answer "did the loop do anything?" without reading every
// event of the pass.
func Cycle(s CycleStats) {
	Event(LevelInfo, "cycle", Fields{
		FieldWatches:  s.Watches,
		FieldReady:    s.Ready,
		FieldRetry:    s.Retry,
		FieldMerges:   s.Merges,
		FieldWakes:    s.Wakes,
		FieldDuration: s.Duration.String(),
	})
}

// VerdictPublished records a verdict the bridge published to the store.
func VerdictPublished(verdict, watchID string, pr int, revision ...uint64) {
	s := Default()
	s.Inc(CounterVerdictPublished, verdict)
	cycles.note(func(c *cycleTracker) {
		if verdict == "ready" {
			c.ready++
		} else {
			c.retry++
		}
	})
	fields := Fields{FieldVerdict: verdict, FieldWatchID: watchID, FieldPR: pr}
	if len(revision) != 0 {
		fields[FieldRevision] = revision[0]
	}
	s.Log(LevelInfo, "verdict_published", fields)
}

// MergeAttempt records an attempted automatic merge.
func MergeAttempt(watchID string, pr int) {
	s := Default()
	s.Inc(CounterMergeAttempts, "")
	s.Log(LevelInfo, "merge_attempt", Fields{FieldWatchID: watchID, FieldPR: pr})
}

// MergeSuccess records a merge the hosting service admitted.
func MergeSuccess(watchID string, pr int) {
	s := Default()
	s.Inc(CounterMergeSuccess, "")
	cycles.note(func(c *cycleTracker) { c.merges++ })
	s.Log(LevelInfo, "merge_success", Fields{FieldWatchID: watchID, FieldPR: pr})
}

// MergeFailure records a merge the hosting service refused.
func MergeFailure(watchID string, pr int, err error) {
	s := Default()
	s.Inc(CounterMergeFailures, "")
	fields := Fields{FieldWatchID: watchID, FieldPR: pr}
	if err != nil {
		fields[FieldError] = err.Error()
	}
	s.Log(LevelWarn, "merge_fail", fields)
}

// MergeDuration records how long one merge took.
func MergeDuration(d time.Duration) {
	Observe(HistogramMerge, d.Seconds())
	Event(LevelDebug, "merge_duration", Fields{FieldDuration: d.String()})
}

// WakeDelivered records one wake the host admitted.
func WakeDelivered(adapter, watchID string, revision ...uint64) {
	s := Default()
	s.Inc(CounterWakeDelivered, adapter)
	cycles.note(func(c *cycleTracker) { c.wakes++ })
	fields := Fields{FieldAdapter: adapter, FieldWatchID: watchID}
	if len(revision) != 0 {
		fields[FieldRevision] = revision[0]
	}
	s.Log(LevelInfo, "wake_delivered", fields)
}

// WakeDuration records how long one wake delivery took.
func WakeDuration(adapter string, d time.Duration) {
	Observe(HistogramWake, d.Seconds())
	Event(LevelDebug, "wake_duration", Fields{FieldAdapter: adapter, FieldDuration: d.String()})
}

// WakeSkipped records a wake that was deliberately not delivered, with the
// reason for the skip. It is a first-class event because "wake не дошёл" is the
// question an operator asks most often.
func WakeSkipped(adapter, watchID, reason string) {
	s := Default()
	s.Inc(CounterWakeSkipped, "")
	cycles.note(func(c *cycleTracker) { c.wakes++ })
	fields := Fields{FieldWatchID: watchID, FieldReason: reason}
	if adapter != "" {
		fields[FieldAdapter] = adapter
	}
	s.Log(LevelInfo, "wake_skipped", fields)
}

// WakeFailed records one wake the host refused or that failed in transport.
func WakeFailed(adapter, watchID string, err error, revision ...uint64) {
	s := Default()
	s.Inc(CounterWakeErrors, adapter)
	cycles.note(func(c *cycleTracker) { c.wakes++ })
	fields := Fields{FieldWatchID: watchID}
	if adapter != "" {
		fields[FieldAdapter] = adapter
	}
	if err != nil {
		fields[FieldError] = err.Error()
	}
	if len(revision) != 0 {
		fields[FieldRevision] = revision[0]
	}
	s.Log(LevelWarn, "wake_error", fields)
}

// RebaseRequested records a pointer asking a subscriber to rebase itself.
func RebaseRequested(watchID string, pr int) {
	s := Default()
	s.Inc(CounterRebaseRequests, "")
	s.Log(LevelInfo, "rebase_requested", Fields{FieldWatchID: watchID, FieldPR: pr})
}

// DependenciesResolved records that every waitFor reference of a Watch is
// satisfied and the merge may proceed.
func DependenciesResolved(watchID string, pr int, refs []string) {
	fields := Fields{FieldWatchID: watchID, FieldPR: pr, FieldRefs: refs}
	Default().Log(LevelDebug, "deps_resolved", fields)
}

// DependencyWait records a Watch held back by an unsatisfied reference.
func DependencyWait(watchID string, pr int, reason string) {
	s := Default()
	s.Inc(CounterDependencyWaits, "")
	s.Log(LevelInfo, "dependency_wait", Fields{
		FieldWatchID: watchID, FieldPR: pr, FieldReason: reason,
	})
}

// BlockDetected records a Watch that waits on outside action.
func BlockDetected(watchID string, pr int, reason string) {
	s := Default()
	s.Inc(CounterBlocks, "")
	fields := Fields{FieldWatchID: watchID, FieldPR: pr}
	if reason != "" {
		fields[FieldReason] = reason
	}
	s.Log(LevelInfo, "blocked_detected", fields)
}

// ActorError records a failure of one named actor in the daemon or a handler.
func ActorError(actor, watchID string, err error) {
	s := Default()
	s.Inc(CounterActorErrors, "")
	fields := Fields{FieldActor: actor}
	if watchID != "" {
		fields[FieldWatchID] = watchID
	}
	if err != nil {
		fields[FieldError] = err.Error()
	}
	s.Log(LevelWarn, "actor_error", fields)
}

// RunStart and RunEnd bracket one orchestrator pass.
func RunStart(watches int) {
	Event(LevelInfo, "run_start", Fields{FieldWatches: watches})
}

// RunEnd records the outcome of one orchestrator pass.
func RunEnd(stats CycleStats, err error) {
	fields := Fields{
		FieldWatches: stats.Watches, FieldMerges: stats.Merges,
		FieldWakes: stats.Wakes, FieldDuration: stats.Duration.String(),
	}
	level := LevelInfo
	if err != nil {
		level = LevelWarn
		fields[FieldError] = err.Error()
	}
	Event(level, "run_end", fields)
	Observe(HistogramRun, stats.Duration.Seconds())
}

// WatchRegistered records a new Watch accepted by a tool handler.
func WatchRegistered(watchID string, pr int, revision uint64, adapter string) {
	fields := Fields{FieldWatchID: watchID, FieldPR: pr, FieldRevision: revision}
	if adapter != "" {
		fields[FieldAdapter] = adapter
	}
	Default().Log(LevelInfo, "watch_registered", fields)
}

// ResultRead records a verdict read back from the store.
func ResultRead(watchID, verdict string, revision uint64) {
	Default().Log(LevelInfo, "result_read", Fields{
		FieldWatchID: watchID, FieldVerdict: verdict, FieldRevision: revision,
	})
}

// Command records one command dispatch in the CLI.
func Command(name string, args []string) {
	Default().Log(LevelDebug, "command_dispatch", Fields{
		FieldCommand: name, "args": args,
	})
}

// ProviderCall records one CI provider call and its outcome.
func ProviderCall(adapter, method, watchID string, err error, d time.Duration) {
	fields := Fields{
		FieldAdapter: adapter, "method": method, FieldWatchID: watchID,
		FieldDuration: d.String(),
	}
	level := LevelDebug
	if err != nil {
		level = LevelWarn
		fields[FieldError] = err.Error()
		ActorError(adapter, watchID, err)
	}
	Default().Log(level, "provider_call", fields)
}

// FlushCycle logs the compact INFO summary of the pass collected so far and
// starts a new one. An idle pass produces no summary line. The CLI calls it on
// exit so a one-shot daemon still reports its pass.
func FlushCycle() {
	stats, started := cycles.flush(time.Now())
	if started || stats.Watches != 0 || stats.Ready != 0 || stats.Retry != 0 || stats.Merges != 0 || stats.Wakes != 0 {
		Cycle(stats)
	}
}

// DaemonPass marks the start of a daemon pass. It flushes the previous pass,
// then leaves the tracker ready for the new one. An idle pass still gets its
// compact summary line.
func DaemonPass() {
	FlushCycle()
	cycles.note(func(c *cycleTracker) { c.started = true })
}
