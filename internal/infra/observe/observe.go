// Package observe provides the small amount of logging and counting the Beacon
// cycle needs right now. It has no heavy dependency and imports no domain or
// adapter package, so a call site in any layer can use it without an import
// cycle. The heavy OpenTelemetry path stays in research.
package observe

import (
	"sync"
	"time"
)

// Level is the severity of one structured event.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Field names of one event. Keeping the keys here avoids typos in the JSON an
// operator greps.
const (
	FieldEvent    = "event"
	FieldWatchID  = "watchID"
	FieldPR       = "pr"
	FieldVerdict  = "verdict"
	FieldAdapter  = "adapter"
	FieldDuration = "duration"
	FieldError    = "error"
)

// Event is one log record. Fields holds every structured value; the logger
// adds time and level. A caller never builds Event directly, it uses the
// helpers below or a Sink.
type Event struct {
	Level  Level
	Fields Fields
}

// Fields are the structured values of one event.
type Fields map[string]any

// Sink is the thin port the Beacon cycle calls. Emit writes one structured
// event, Inc adds one to a named counter with an optional label value. The
// global default is a no-op until the composition root installs a Recorder, so
// a call site never needs a nil check and existing tests keep working.
type Sink interface {
	Emit(event string, level Level, fields Fields)
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

// nopSink drops every event and count.
type nopSink struct{}

func (nopSink) Emit(string, Level, Fields) {}
func (nopSink) Inc(string, string)        {}

// Recorder combines a Logger and Metrics into one Sink. A nil field is safe:
// the recorder simply skips that half.
type Recorder struct {
	log     *Logger
	metrics *Metrics
}

// NewRecorder builds a Recorder from a logger and a metric set.
func NewRecorder(log *Logger, metrics *Metrics) *Recorder {
	return &Recorder{log: log, metrics: metrics}
}

// Logger returns the recorder's logger. It may be nil.
func (r *Recorder) Logger() *Logger {
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

// Emit writes one structured event through the recorder's logger.
func (r *Recorder) Emit(event string, level Level, fields Fields) {
	if r == nil || r.log == nil {
		return
	}
	merged := make(Fields, len(fields)+1)
	for k, v := range fields {
		merged[k] = v
	}
	merged[FieldEvent] = event
	r.log.Write(Event{Level: level, Fields: merged})
}

// Inc adds one to a counter through the recorder's metric set.
func (r *Recorder) Inc(name, labelValue string) {
	if r == nil || r.metrics == nil {
		return
	}
	r.metrics.Inc(name, labelValue)
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
	Default().Emit("poll_cycle", LevelDebug, Fields{FieldDuration: d.String()})
}

// VerdictPublished records a verdict the bridge published to the store.
func VerdictPublished(verdict, watchID string, pr int) {
	s := Default()
	s.Inc(CounterVerdictPublished, verdict)
	s.Emit("verdict_published", LevelInfo, Fields{
		FieldVerdict: verdict,
		FieldWatchID: watchID,
		FieldPR:      pr,
	})
}

// MergeAttempt records an attempted automatic merge.
func MergeAttempt(watchID string, pr int) {
	s := Default()
	s.Inc(CounterMergeAttempt, "")
	s.Emit("merge_attempt", LevelInfo, Fields{FieldWatchID: watchID, FieldPR: pr})
}

// MergeSuccess records a merge the hosting service admitted.
func MergeSuccess(watchID string, pr int) {
	s := Default()
	s.Inc(CounterMergeSuccess, "")
	s.Emit("merge_success", LevelInfo, Fields{FieldWatchID: watchID, FieldPR: pr})
}

// WakeDelivered records one wake the host admitted.
func WakeDelivered(adapter, watchID string) {
	s := Default()
	s.Inc(CounterWakeDelivered, adapter)
	s.Emit("wake_delivered", LevelInfo, Fields{FieldAdapter: adapter, FieldWatchID: watchID})
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
	s.Emit("wake_failed", LevelWarn, fields)
}

// RebaseRequested records a pointer asking a subscriber to rebase itself.
func RebaseRequested(watchID string, pr int) {
	s := Default()
	s.Inc(CounterRebaseRequested, "")
	s.Emit("rebase_requested", LevelInfo, Fields{FieldWatchID: watchID, FieldPR: pr})
}

// DaemonPass records one pass of the daemon loop.
func DaemonPass() { Default().Emit("daemon_pass", LevelDebug, nil) }

// LogEvent writes an unstructured call site's event through the installed sink.
func LogEvent(event string, level Level, fields Fields) {
	Default().Emit(event, level, fields)
}
