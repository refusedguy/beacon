// The tracing seam in this file mirrors the OpenTelemetry API: Start returns a
// child context and an end function, so an OTLP provider can replace it later
// without touching callers. The default tracer is a no-op: adding a span costs
// one interface call and never changes the context.
package observe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// EnabledEnv is the feature flag. Only the literal "true" enables tracing;
	// "1" and other truthy strings stay off, as in the OTel SDK kill switch.
	EnabledEnv = "BEACON_OTEL_ENABLED"
	// ExporterEnv is reserved for the future OTLP exporter. It is read into
	// Config but does not select an exporter yet.
	ExporterEnv = "BEACON_OTEL_EXPORTER"

	// TraceFlagSampled is the sampled bit of the W3C trace flags byte.
	TraceFlagSampled = 0x01
)

// Attr is one string attribute. Typed attributes and semantic-convention helpers
// belong to a future OTLP adapter; keeping this one type avoids a heavy SDK.
type Attr struct {
	Key   string
	Value string
}

// String builds a string attribute.
func String(key, value string) Attr { return Attr{Key: key, Value: value} }

// SpanContext carries W3C trace identifiers. Child tracers read it from the
// context passed to Start, which is how a span becomes a child without a
// package-level parent stack.
type SpanContext struct {
	TraceID [16]byte
	SpanID  [8]byte
	Flags   byte
}

// IsValid reports whether the identifiers can parent a child span.
func (s SpanContext) IsValid() bool {
	return !allZero(s.TraceID[:]) && !allZero(s.SpanID[:])
}

// TraceParent renders the W3C traceparent header, or an empty string for an
// invalid context. It lets a future client propagate the trace over HTTP.
func (s SpanContext) TraceParent() string {
	if !s.IsValid() {
		return ""
	}
	return "00-" + hex.EncodeToString(s.TraceID[:]) + "-" +
		hex.EncodeToString(s.SpanID[:]) + "-" + hex.EncodeToString([]byte{s.Flags & 0x0f})
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// Span is a finished span handed to a SpanSink. High-cardinality values belong
// here; metrics keep only low-cardinality attributes.
type Span struct {
	Name         string
	Ctx          SpanContext
	ParentSpanID [8]byte
	Start        time.Time
	End          time.Time
	Attrs        []Attr
}

// Duration is the recorded wall time of the span.
func (s Span) Duration() time.Duration { return s.End.Sub(s.Start) }

// SpanSink receives finished spans. The future OTLP exporter implements it.
type SpanSink interface {
	Emit(Span)
}

// Tracer starts spans. A no-op implementation returns the context unchanged
// and an end function that does nothing.
type Tracer interface {
	Start(ctx context.Context, name string, attrs ...Attr) (context.Context, func())
}

// Config is the complete configuration of the local tracer.
type Config struct {
	Enabled  bool
	Exporter string
	SpanSink SpanSink
}

// ConfigFromEnv reads the two Beacon variables. An empty exporter is left
// empty; the flag is strict so an accidental "1" cannot enable tracing.
func ConfigFromEnv() Config {
	return Config{
		Enabled:  strings.EqualFold(strings.TrimSpace(os.Getenv(EnabledEnv)), "true"),
		Exporter: strings.TrimSpace(os.Getenv(ExporterEnv)),
	}
}

type spanContextKey struct{}

// SpanContextFromContext returns the active span context, if any. A no-op
// tracer never installs one, so this also tells callers whether tracing is on.
func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	return sc, ok && sc.IsValid()
}

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ ...Attr) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, func() {}
}

type traceTracer struct {
	sink SpanSink
}

// NewTracer returns a no-op tracer unless cfg.Enabled is set. An enabled
// tracer with no sink writes each finished span through the existing observe
// sink, so spans land in the same run log as slog records and counters.
func NewTracer(cfg Config) Tracer {
	if !cfg.Enabled {
		return noopTracer{}
	}
	sink := cfg.SpanSink
	if sink == nil {
		sink = observeSink{}
	}
	return &traceTracer{sink: sink}
}

func (t *traceTracer) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	parent, hasParent := SpanContextFromContext(ctx)
	sc := SpanContext{TraceID: newTraceID(), SpanID: newSpanID(), Flags: TraceFlagSampled}
	if hasParent {
		sc.TraceID = parent.TraceID
		sc.Flags = parent.Flags
	}
	span := Span{
		Name:        name,
		Ctx:         sc,
		Start:       time.Now(),
		Attrs:       append([]Attr(nil), attrs...),
	}
	if hasParent {
		span.ParentSpanID = parent.SpanID
	}
	child := context.WithValue(ctx, spanContextKey{}, sc)
	var once sync.Once
	return child, func() {
		once.Do(func() {
			span.End = time.Now()
			t.sink.Emit(span)
		})
	}
}

var idFallback atomic.Uint64

func newTraceID() (id [16]byte) {
	fillID(id[:])
	return id
}

func newSpanID() (id [8]byte) {
	fillID(id[:])
	return id
}

func fillID(dst []byte) {
	if _, err := rand.Read(dst); err == nil {
		return
	}
	// crypto/rand failing is rare; a time-and-counter mix still gives unique,
	// non-zero IDs without a second dependency.
	seed := time.Now().UnixNano() ^ int64(idFallback.Add(1))
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(seed))
	for i := range dst {
		dst[i] = buf[i%len(buf)] ^ byte(i+int(seed))
	}
	if allZero(dst) {
		dst[0] = 1
	}
}

// observeSink writes finished spans through the existing structured-log seam.
type observeSink struct{}

func (observeSink) Emit(s Span) {
	fields := Fields{
		"span.name":   s.Name,
		"trace.id":    hex.EncodeToString(s.Ctx.TraceID[:]),
		"span.id":    hex.EncodeToString(s.Ctx.SpanID[:]),
		"duration.us": s.Duration().Microseconds(),
	}
	if !allZero(s.ParentSpanID[:]) {
		fields["parent.id"] = hex.EncodeToString(s.ParentSpanID[:])
	}
	for _, a := range s.Attrs {
		fields[a.Key] = a.Value
	}
	Default().Log(LevelInfo, "otel.span", fields)
}

// SpanRecorder is an in-memory SpanSink for tests and for callers that want to inspect
// spans without an exporter.
type SpanRecorder struct {
	mu    sync.Mutex
	spans []Span
}

func (r *SpanRecorder) Emit(s Span) {
	r.mu.Lock()
	r.spans = append(r.spans, s)
	r.mu.Unlock()
}

// Spans returns a copy of the finished spans in emission order.
func (r *SpanRecorder) Spans() []Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Span(nil), r.spans...)
}

var (
	currentMu sync.RWMutex
	current   Tracer
)

func init() {
	current = NewTracer(ConfigFromEnv())
}

// Current returns the process tracer. It is read per call so a future OTLP
// provider can be installed without restarting the process.
func Current() Tracer {
	currentMu.RLock()
	t := current
	currentMu.RUnlock()
	return t
}

// SetTracer installs t and returns a function that restores the previous
// tracer. A nil t installs the no-op tracer.
func SetTracer(t Tracer) func() {
	if t == nil {
		t = noopTracer{}
	}
	currentMu.Lock()
	previous := current
	current = t
	currentMu.Unlock()
	return func() {
		currentMu.Lock()
		current = previous
		currentMu.Unlock()
	}
}
