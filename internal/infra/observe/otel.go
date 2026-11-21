// The tracing seam in this file mirrors the OpenTelemetry API: Start returns a
// child context and an end function, so an OTLP sink can replace the local one
// without touching callers. The default tracer is a no-op: adding a span costs
// one interface call and never changes the context.
package observe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// EnabledEnv is the feature flag. Only the literal "true" enables tracing;
	// "1" and other truthy strings stay off, as in the OTel SDK kill switch.
	EnabledEnv = "BEACON_OTEL_ENABLED"
	// ExporterEnv selects the sink. The value "otlp" asks for the OTLP exporter;
	// any other value keeps spans in the local slog sink.
	ExporterEnv = "BEACON_OTEL_EXPORTER"
	// OTLPEndpointEnv is the collector base URL, for example http://host:4318.
	// An empty value keeps spans local even when ExporterEnv asks for OTLP.
	OTLPEndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// TracesEndpointEnv names the traces URL directly and wins over the base URL.
	TracesEndpointEnv = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	// OTLPProtocolEnv selects the wire encoding. Supported values are
	// http/protobuf and http/json. grpc is recognized but unsupported in this
	// stdlib-only build: NewOTLPExporter rejects it with an explicit error.
	// See normalizeProtocol.
	OTLPProtocolEnv = "OTEL_EXPORTER_OTLP_PROTOCOL"

	// TraceFlagSampled is the sampled bit of the W3C trace flags byte.
	TraceFlagSampled = 0x01

	// Protocol values recognized by Config.Protocol and OTLPConfig.Protocol.
	// ProtocolGRPC is recognized for a clear error, not for export.
	ProtocolHTTPProtobuf = "http/protobuf"
	ProtocolHTTPJSON     = "http/json"
	ProtocolGRPC         = "grpc"

	defaultBatchSize   = 64
	defaultOTLPTimeout = 2 * time.Second
	maxResponseDrain   = 1 << 20
)

// normalizeProtocol maps a configured protocol onto the supported values.
// Empty and unknown values fall back to http/protobuf, the OTLP default.
func normalizeProtocol(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ProtocolHTTPJSON, "json":
		return ProtocolHTTPJSON
	case ProtocolGRPC:
		return ProtocolGRPC
	default:
		return ProtocolHTTPProtobuf
	}
}

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

// SpanSink receives finished spans. Both the local logger and OTLPExporter
// implement it.
type SpanSink interface {
	Emit(Span)
}

// Tracer starts spans. A no-op implementation returns the context unchanged
// and an end function that does nothing.
type Tracer interface {
	Start(ctx context.Context, name string, attrs ...Attr) (context.Context, func())
}

// Config is the complete configuration of the tracer. Endpoint and Protocol
// describe the optional OTLP exporter. An empty Endpoint selects local-only
// spans even when Exporter asks for OTLP.
type Config struct {
	Enabled  bool
	Exporter string
	Endpoint string
	Protocol string
	SpanSink SpanSink
}

// WarnConfig reports an environment value that the reader replaced with a
// default. The standard logger reaches stderr before the composition root
// installs the log writer, then reaches the same rotating log as the other
// legacy log.Printf calls. The operator sees the variable name and the value
// that was ignored.
func WarnConfig(env, value, reason string) {
	msg := fmt.Sprintf("beacon: неверный %s=%q", env, value)
	if reason != "" {
		msg += ": " + reason
	}
	log.Print(msg)
}

// ConfigFromEnv reads the Beacon flag, the exporter name and the standard OTLP
// environment variables. The flag is strict so an accidental "1" cannot enable
// tracing. An empty protocol means http/protobuf. An unsupported exporter or
// protocol produces a warning and keeps the local sink.
func ConfigFromEnv() Config {
	endpoint := strings.TrimSpace(os.Getenv(OTLPEndpointEnv))
	if direct := strings.TrimSpace(os.Getenv(TracesEndpointEnv)); direct != "" {
		endpoint = direct
	}
	exporter := strings.TrimSpace(os.Getenv(ExporterEnv))
	if exporter != "" && !strings.EqualFold(exporter, "otlp") {
		WarnConfig(ExporterEnv, exporter, "поддерживается только otlp, используется локальный sink")
	}
	protocolRaw := strings.TrimSpace(os.Getenv(OTLPProtocolEnv))
	switch strings.ToLower(protocolRaw) {
	case "", "http/protobuf", "http/json", "json", ProtocolGRPC:
	default:
		WarnConfig(OTLPProtocolEnv, protocolRaw, "неизвестный протокол, используется "+ProtocolHTTPProtobuf)
	}
	return Config{
		Enabled:  strings.EqualFold(strings.TrimSpace(os.Getenv(EnabledEnv)), "true"),
		Exporter: exporter,
		Endpoint: endpoint,
		Protocol: normalizeProtocol(protocolRaw),
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

// NewTracer returns a no-op tracer unless cfg.Enabled is set. An enabled tracer
// uses cfg.SpanSink when the caller supplies one. Otherwise it picks the OTLP
// exporter for a configured endpoint and falls back to the local observe sink.
// Switching sinks never changes Tracer.Start.
func NewTracer(cfg Config) Tracer {
	if !cfg.Enabled {
		return noopTracer{}
	}
	sink := cfg.SpanSink
	if sink == nil {
		sink = sinkFromConfig(cfg)
	}
	return &traceTracer{sink: sink}
}

// sinkFromConfig chooses where finished spans go. A request to export without
// an endpoint, or a gRPC request in this dependency-free build, keeps spans in
// the local run log and records one warning.
func sinkFromConfig(cfg Config) SpanSink {
	if !strings.EqualFold(strings.TrimSpace(cfg.Exporter), "otlp") ||
		strings.TrimSpace(cfg.Endpoint) == "" {
		return observeSink{}
	}
	if normalizeProtocol(cfg.Protocol) == ProtocolGRPC {
		logOTLPFallback("grpc needs an external gRPC dependency", nil)
		return observeSink{}
	}
	exp, err := NewOTLPExporter(OTLPConfig{Endpoint: cfg.Endpoint, Protocol: cfg.Protocol})
	if err != nil {
		logOTLPFallback("cannot create OTLP exporter", err)
		return observeSink{}
	}
	activeExporterMu.Lock()
	activeExporter = exp
	activeExporterMu.Unlock()
	return exp
}

func logOTLPFallback(reason string, err error) {
	fields := Fields{"reason": reason}
	if err != nil {
		fields["error"] = err.Error()
	}
	Default().Log(LevelWarn, "otel.local_fallback", fields)
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
		Name:  name,
		Ctx:   sc,
		Start: time.Now(),
		Attrs: append([]Attr(nil), attrs...),
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
		"span.id":     hex.EncodeToString(s.Ctx.SpanID[:]),
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

// OTLPConfig configures the dependency-free OTLP/HTTP exporter. Endpoint is a
// collector base URL; the exporter posts to /v1/traces unless Endpoint already
// ends with that path. Client and BatchSize are optional seams for tests.
type OTLPConfig struct {
	Endpoint  string
	Protocol  string
	Client    *http.Client
	BatchSize int
	Timeout   time.Duration
}

// OTLPExporter batches finished spans and posts them to an OTLP/HTTP collector.
// It implements SpanSink, so installing it never changes Tracer.Start. An
// unreachable collector only produces a warning in the local log; the command
// that emitted the span keeps running.
type OTLPExporter struct {
	endpoint  string
	protocol  string
	client    *http.Client
	batchSize int

	mu     sync.Mutex
	batch  []Span
	closed bool
}

// NewOTLPExporter validates cfg and returns a ready exporter. An empty endpoint
// or the gRPC protocol returns an explicit error. No supported protocol is
// silently downgraded: callers that ignore the error keep spans local.
func NewOTLPExporter(cfg OTLPConfig) (*OTLPExporter, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("otlp: invalid endpoint %q", endpoint)
	}
	protocol := normalizeProtocol(cfg.Protocol)
	if protocol == ProtocolGRPC {
		return nil, fmt.Errorf("otlp: grpc transport needs an external dependency")
	}
	client := cfg.Client
	if client == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = defaultOTLPTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	size := cfg.BatchSize
	if size <= 0 {
		size = defaultBatchSize
	}
	return &OTLPExporter{
		endpoint:  endpoint,
		protocol:  protocol,
		client:    client,
		batchSize: size,
	}, nil
}

// tracesURL returns the full OTLP traces endpoint, adding /v1/traces to a base
// URL when needed.
func (e *OTLPExporter) tracesURL() string {
	base := strings.TrimRight(e.endpoint, "/")
	if strings.HasSuffix(base, "/v1/traces") {
		return base
	}
	return base + "/v1/traces"
}

// Emit buffers a finished span and posts the batch once it is full. Overflow
// never blocks the caller longer than the configured HTTP timeout.
func (e *OTLPExporter) Emit(s Span) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.batch = append(e.batch, s)
	full := len(e.batch) >= e.batchSize
	e.mu.Unlock()
	if full {
		if err := e.Flush(context.Background()); err != nil {
			logOTLPFallback("flush on full batch failed", err)
		}
	}
}

// Flush posts the buffered spans as one request and clears the buffer. It is
// safe to call with an empty buffer.
func (e *OTLPExporter) Flush(ctx context.Context) error {
	e.mu.Lock()
	if len(e.batch) == 0 {
		e.mu.Unlock()
		return nil
	}
	batch := e.batch
	e.batch = nil
	e.mu.Unlock()

	body, contentType, err := encodeOTLPBatch(batch, e.protocol)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tracesURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("otlp: collector returned %s", resp.Status)
	}
	return nil
}

// Shutdown flushes the remaining batch and closes idle connections. Callers
// invoke it on process exit. A dead collector returns an error; the CLI treats
// that unconfirmed drain as a failed shutdown and exits nonzero.
func (e *OTLPExporter) Shutdown(ctx context.Context) error {
	err := e.Flush(ctx)
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.client.CloseIdleConnections()
	return err
}

// Shutdowner is implemented by exporters that flush on exit.
type Shutdowner interface {
	Shutdown(context.Context) error
}

var (
	activeExporterMu sync.RWMutex
	activeExporter   Shutdowner
)

// Shutdown flushes the exporter installed by NewTracer. With local-only or
// disabled tracing there is no exporter, so the call does nothing.
func Shutdown(ctx context.Context) error {
	activeExporterMu.RLock()
	exp := activeExporter
	activeExporterMu.RUnlock()
	if exp == nil {
		return nil
	}
	return exp.Shutdown(ctx)
}

// encodeOTLPBatch builds the body for one export request. Both HTTP encodings
// are produced in this file so go.mod stays unchanged.
func encodeOTLPBatch(spans []Span, protocol string) ([]byte, string, error) {
	switch normalizeProtocol(protocol) {
	case ProtocolHTTPJSON:
		body, err := json.Marshal(otlpJSONRequestFromSpans(spans))
		return body, "application/json", err
	case ProtocolGRPC:
		return nil, "", fmt.Errorf("otlp: grpc transport needs an external dependency")
	default:
		return otlpProtoRequest(spans), "application/x-protobuf", nil
	}
}

// The JSON structs follow the OTLP protobuf JSON mapping: camelCase field
// names, 64-bit timestamps as strings and hex trace identifiers.
type otlpJSONRequest struct {
	ResourceSpans []otlpJSONResourceSpans `json:"resourceSpans"`
}

type otlpJSONResourceSpans struct {
	Resource   otlpJSONResource     `json:"resource"`
	ScopeSpans []otlpJSONScopeSpans `json:"scopeSpans"`
}

type otlpJSONResource struct {
	Attributes []otlpJSONKeyValue `json:"attributes"`
}

type otlpJSONScopeSpans struct {
	Scope otlpJSONScope  `json:"scope"`
	Spans []otlpJSONSpan `json:"spans"`
}

type otlpJSONScope struct {
	Name string `json:"name"`
}

type otlpJSONSpan struct {
	TraceID      string             `json:"traceId"`
	SpanID       string             `json:"spanId"`
	ParentSpanID string             `json:"parentSpanId,omitempty"`
	Name         string             `json:"name"`
	StartTime    string             `json:"startTimeUnixNano"`
	EndTime      string             `json:"endTimeUnixNano"`
	Attributes   []otlpJSONKeyValue `json:"attributes,omitempty"`
	Status       otlpJSONStatus     `json:"status"`
}

type otlpJSONStatus struct {
	Code int `json:"code"`
}

type otlpJSONKeyValue struct {
	Key   string           `json:"key"`
	Value otlpJSONAnyValue `json:"value"`
}

type otlpJSONAnyValue struct {
	StringValue string `json:"stringValue"`
}

func otlpJSONRequestFromSpans(spans []Span) otlpJSONRequest {
	attrs := func(in []Attr) []otlpJSONKeyValue {
		if len(in) == 0 {
			return nil
		}
		out := make([]otlpJSONKeyValue, 0, len(in))
		for _, a := range in {
			out = append(out, otlpJSONKeyValue{
				Key:   a.Key,
				Value: otlpJSONAnyValue{StringValue: a.Value},
			})
		}
		return out
	}

	jsonSpans := make([]otlpJSONSpan, 0, len(spans))
	for _, s := range spans {
		js := otlpJSONSpan{
			TraceID:    hex.EncodeToString(s.Ctx.TraceID[:]),
			SpanID:     hex.EncodeToString(s.Ctx.SpanID[:]),
			Name:       s.Name,
			StartTime:  strconv.FormatInt(s.Start.UnixNano(), 10),
			EndTime:    strconv.FormatInt(s.End.UnixNano(), 10),
			Attributes: attrs(s.Attrs),
			Status:     otlpJSONStatus{Code: 1},
		}
		if !allZero(s.ParentSpanID[:]) {
			js.ParentSpanID = hex.EncodeToString(s.ParentSpanID[:])
		}
		jsonSpans = append(jsonSpans, js)
	}
	return otlpJSONRequest{
		ResourceSpans: []otlpJSONResourceSpans{{
			Resource: otlpJSONResource{Attributes: []otlpJSONKeyValue{{
				Key:   "service.name",
				Value: otlpJSONAnyValue{StringValue: "beacon"},
			}}},
			ScopeSpans: []otlpJSONScopeSpans{{
				Scope: otlpJSONScope{Name: "beacon/observe"},
				Spans: jsonSpans,
			}},
		}},
	}
}

// The protobuf helpers encode only the OTLP trace fields this tracer emits:
// string attributes, W3C IDs, nanosecond timestamps and an OK status. Protobuf
// field numbers and wire types come from the stable OTLP trace schema.
func otlpProtoRequest(spans []Span) []byte {
	resourceAttrs := protoStringAttr("service.name", "beacon")
	var resource []byte
	resource = appendProtoBytes(resource, 1, resourceAttrs)

	scope := appendProtoString(nil, 1, "beacon/observe")
	var scopeSpans []byte
	scopeSpans = appendProtoBytes(scopeSpans, 1, scope)
	for _, s := range spans {
		scopeSpans = appendProtoBytes(scopeSpans, 2, protoSpan(s))
	}

	var resourceSpans []byte
	resourceSpans = appendProtoBytes(resourceSpans, 1, resource)
	resourceSpans = appendProtoBytes(resourceSpans, 2, scopeSpans)

	var req []byte
	return appendProtoBytes(req, 1, resourceSpans)
}

func protoSpan(s Span) []byte {
	var b []byte
	b = appendProtoBytes(b, 1, s.Ctx.TraceID[:])
	b = appendProtoBytes(b, 2, s.Ctx.SpanID[:])
	if !allZero(s.ParentSpanID[:]) {
		b = appendProtoBytes(b, 4, s.ParentSpanID[:])
	}
	b = appendProtoString(b, 5, s.Name)
	b = appendProtoFixed64(b, 7, uint64(s.Start.UnixNano()))
	b = appendProtoFixed64(b, 8, uint64(s.End.UnixNano()))
	for _, a := range s.Attrs {
		b = appendProtoBytes(b, 9, protoStringAttr(a.Key, a.Value))
	}
	status := appendProtoKey(nil, 3, 0)
	status = appendProtoVarint(status, 1)
	return appendProtoBytes(b, 15, status)
}

func protoStringAttr(key, value string) []byte {
	kv := appendProtoString(nil, 1, key)
	av := appendProtoString(nil, 1, value)
	return appendProtoBytes(kv, 2, av)
}

func appendProtoVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func appendProtoKey(dst []byte, field, wire int) []byte {
	return appendProtoVarint(dst, uint64(field)<<3|uint64(wire))
}

func appendProtoBytes(dst []byte, field int, b []byte) []byte {
	dst = appendProtoKey(dst, field, 2)
	dst = appendProtoVarint(dst, uint64(len(b)))
	return append(dst, b...)
}

func appendProtoString(dst []byte, field int, s string) []byte {
	return appendProtoBytes(dst, field, []byte(s))
}

func appendProtoFixed64(dst []byte, field int, v uint64) []byte {
	dst = appendProtoKey(dst, field, 1)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(dst, buf[:]...)
}

var (
	currentMu sync.RWMutex
	current   Tracer
)

func init() {
	current = NewTracer(ConfigFromEnv())
}

// Current returns the process tracer. It is read per call so the CLI can
// install an OTLP tracer at startup without restarting the process.
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
