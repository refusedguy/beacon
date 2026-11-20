// Package observe provides the small amount of logging and counting the Beacon
// cycle needs right now. Logging goes through the standard library log/slog,
// with no heavy dependency. The package imports no domain or adapter package,
// so a call site in any layer can use it without an import cycle. The heavy
// OpenTelemetry path stays in research.
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
	FieldEvent    = "event"
	FieldWatchID  = "watchID"
	FieldPR       = "pr"
	FieldVerdict  = "verdict"
	FieldAdapter  = "adapter"
	FieldDuration = "duration"
	FieldError    = "error"
)

// Fields are the structured values of one event.
type Fields map[string]any

// Sink is the thin port the Beacon cycle calls. Log writes one structured
// record through slog, Inc adds one to a named counter with an optional label
// value. The global default is a no-op until the composition root installs a
// Recorder, so a call site never needs a nil check and existing tests keep
// working.
type Sink interface {
	Log(level Level, event string, fields Fields)
	Inc(name, labelValue string)
}

// Counter names exported by Metrics.
const (
	CounterPollCycles       = "poll_cycles"
	CounterVerdictPublished = "verdict_published"
	CounterMergeAttempt     = "merge_attempt"
	CounterMergeSuccess     = "merge_success"
	CounterWakeDelivered    = "wake_delivered"
	CounterWakeFailed       = "wake_failed"
	CounterRebaseRequested  = "rebase_requested"
)

// Label keys of the labelled counters.
const (
	LabelVerdict = "verdict"
	LabelAdapter = "adapter"
)

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

func (nopSink) Log(Level, string, Fields) {}
func (nopSink) Inc(string, string)       {}

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

// PollCycle records one CI poll.
func PollCycle() { Default().Inc(CounterPollCycles, "") }

// PollCycleDuration logs how long one CI poll took. It does not change the
// counter; PollCycle already did that.
func PollCycleDuration(d time.Duration) {
	Default().Log(LevelDebug, "poll_cycle", Fields{FieldDuration: d.String()})
}

// VerdictPublished records a verdict the bridge published to the store.
func VerdictPublished(verdict, watchID string, pr int) {
	s := Default()
	s.Inc(CounterVerdictPublished, verdict)
	s.Log(LevelInfo, "verdict_published", Fields{
		FieldVerdict: verdict,
		FieldWatchID: watchID,
		FieldPR:      pr,
	})
}

// MergeAttempt records an attempted automatic merge.
func MergeAttempt(watchID string, pr int) {
	s := Default()
	s.Inc(CounterMergeAttempt, "")
	s.Log(LevelInfo, "merge_attempt", Fields{FieldWatchID: watchID, FieldPR: pr})
}

// MergeSuccess records a merge the hosting service admitted.
func MergeSuccess(watchID string, pr int) {
	s := Default()
	s.Inc(CounterMergeSuccess, "")
	s.Log(LevelInfo, "merge_success", Fields{FieldWatchID: watchID, FieldPR: pr})
}

// WakeDelivered records one wake the host admitted.
func WakeDelivered(adapter, watchID string) {
	s := Default()
	s.Inc(CounterWakeDelivered, adapter)
	s.Log(LevelInfo, "wake_delivered", Fields{FieldAdapter: adapter, FieldWatchID: watchID})
}

// WakeFailed records one wake the host refused or that failed in transport.
func WakeFailed(adapter, watchID string, err error) {
	s := Default()
	s.Inc(CounterWakeFailed, "")
	fields := Fields{FieldWatchID: watchID}
	if adapter != "" {
		fields[FieldAdapter] = adapter
	}
	if err != nil {
		fields[FieldError] = err.Error()
	}
	s.Log(LevelWarn, "wake_failed", fields)
}

// RebaseRequested records a pointer asking a subscriber to rebase itself.
func RebaseRequested(watchID string, pr int) {
	s := Default()
	s.Inc(CounterRebaseRequested, "")
	s.Log(LevelInfo, "rebase_requested", Fields{FieldWatchID: watchID, FieldPR: pr})
}

// DaemonPass records one pass of the daemon loop.
func DaemonPass() { Default().Log(LevelDebug, "daemon_pass", nil) }

// LogEvent writes an unstructured call site's event through the installed sink.
func LogEvent(event string, level Level, fields Fields) {
	Default().Log(level, event, fields)
}
