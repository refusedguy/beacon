package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func otlpTestSpan(name string) Span {
	start := time.Unix(1_700_000_000, 0)
	return Span{
		Name:  name,
		Ctx:   SpanContext{TraceID: [16]byte{1, 2, 3}, SpanID: [8]byte{4, 5, 6}, Flags: TraceFlagSampled},
		Start: start,
		End:   start.Add(time.Millisecond),
		Attrs: []Attr{String("beacon.component", "daemon")},
	}
}

type capturedRequest struct {
	path        string
	contentType string
	body        []byte
}

// recordingCollector is an httptest server that keeps every export request.
type recordingCollector struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
}

func newRecordingCollector(t *testing.T) *recordingCollector {
	t.Helper()
	c := &recordingCollector{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.requests = append(c.requests, capturedRequest{
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		})
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *recordingCollector) snapshot() []capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedRequest(nil), c.requests...)
}

func TestConfigFromEnvReadsOTLPFields(t *testing.T) {
	t.Setenv(EnabledEnv, "true")
	t.Setenv(ExporterEnv, "otlp")
	t.Setenv(OTLPEndpointEnv, "http://collector:4318")
	t.Setenv(TracesEndpointEnv, "")
	t.Setenv(OTLPProtocolEnv, "http/json")

	cfg := ConfigFromEnv()
	if !cfg.Enabled || cfg.Exporter != "otlp" {
		t.Fatalf("flag/exporter = %v/%q", cfg.Enabled, cfg.Exporter)
	}
	if cfg.Endpoint != "http://collector:4318" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.Protocol != ProtocolHTTPJSON {
		t.Fatalf("protocol = %q, want %q", cfg.Protocol, ProtocolHTTPJSON)
	}
}

func TestConfigFromEnvTracesEndpointWins(t *testing.T) {
	t.Setenv(OTLPEndpointEnv, "http://base:4318")
	t.Setenv(OTLPProtocolEnv, "http/protobuf")
	t.Setenv(TracesEndpointEnv, "http://direct:4318/v1/traces")
	cfg := ConfigFromEnv()
	if cfg.Endpoint != "http://direct:4318/v1/traces" {
		t.Fatalf("endpoint = %q, direct traces URL should win", cfg.Endpoint)
	}
}

func TestOTLPExporterProtoSendsBatchToMockCollector(t *testing.T) {
	collector := newRecordingCollector(t)
	exp, err := NewOTLPExporter(OTLPConfig{
		Endpoint:  collector.URL,
		Protocol:  ProtocolHTTPProtobuf,
		BatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	exp.Emit(otlpTestSpan("beacon.daemon.tick"))
	if got := len(collector.snapshot()); got != 0 {
		t.Fatalf("sent %d requests before batch was full", got)
	}
	exp.Emit(otlpTestSpan("beacon.wake.deliver"))
	if got := len(collector.snapshot()); got != 1 {
		t.Fatalf("sent %d requests, want 1", got)
	}
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(collector.snapshot()); got != 1 {
		t.Fatalf("shutdown sent an extra request: %d", got)
	}

	req := collector.snapshot()[0]
	if req.path != "/v1/traces" {
		t.Errorf("path = %q, want /v1/traces", req.path)
	}
	if req.contentType != "application/x-protobuf" {
		t.Errorf("content type = %q", req.contentType)
	}
	for _, want := range []string{"beacon.daemon.tick", "beacon.wake.deliver", "service.name", "beacon/observe"} {
		if !bytes.Contains(req.body, []byte(want)) {
			t.Errorf("protobuf body does not contain %q", want)
		}
	}
}

func TestOTLPExporterShutdownFlushesRemainder(t *testing.T) {
	collector := newRecordingCollector(t)
	exp, err := NewOTLPExporter(OTLPConfig{
		Endpoint:  collector.URL,
		Protocol:  ProtocolHTTPProtobuf,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	exp.Emit(otlpTestSpan("beacon.orchestrator.pass"))
	if got := len(collector.snapshot()); got != 0 {
		t.Fatalf("sent %d requests before shutdown", got)
	}
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := collector.snapshot()
	if len(requests) != 1 {
		t.Fatalf("sent %d requests after shutdown, want 1", len(requests))
	}
	if !bytes.Contains(requests[0].body, []byte("beacon.orchestrator.pass")) {
		t.Fatal("flushed body lost the span name")
	}
	exp.Emit(otlpTestSpan("beacon.after.shutdown"))
	if got := len(collector.snapshot()); got != 1 {
		t.Fatalf("emit after shutdown sent a request: %d", got)
	}
}

func TestOTLPExporterJSONPostsReadableBody(t *testing.T) {
	collector := newRecordingCollector(t)
	exp, err := NewOTLPExporter(OTLPConfig{Endpoint: collector.URL, Protocol: ProtocolHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	exp.Emit(otlpTestSpan("beacon.ci.refresh"))
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := collector.snapshot()
	if len(requests) != 1 {
		t.Fatalf("sent %d requests, want 1", len(requests))
	}
	req := requests[0]
	if req.contentType != "application/json" {
		t.Errorf("content type = %q", req.contentType)
	}
	var decoded otlpJSONRequest
	if err := json.Unmarshal(req.body, &decoded); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}
	if len(decoded.ResourceSpans) != 1 {
		t.Fatalf("resourceSpans = %d", len(decoded.ResourceSpans))
	}
	rs := decoded.ResourceSpans[0]
	if len(rs.Resource.Attributes) != 1 || rs.Resource.Attributes[0].Value.StringValue != "beacon" {
		t.Fatalf("resource attributes = %+v", rs.Resource.Attributes)
	}
	if len(rs.ScopeSpans) != 1 || rs.ScopeSpans[0].Scope.Name != "beacon/observe" {
		t.Fatalf("scopeSpans = %+v", rs.ScopeSpans)
	}
	spans := rs.ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	span := spans[0]
	if span.Name != "beacon.ci.refresh" {
		t.Errorf("span name = %q", span.Name)
	}
	if span.TraceID != "01020300000000000000000000000000" {
		t.Errorf("trace ID = %q", span.TraceID)
	}
	if span.SpanID != "0405060000000000" {
		t.Errorf("span ID = %q", span.SpanID)
	}
	if span.StartTime == "" || span.EndTime == "" {
		t.Fatal("timestamps are not rendered as strings")
	}
	if span.Status.Code != 1 {
		t.Errorf("status code = %d, want 1", span.Status.Code)
	}
}

func TestSinkFromConfigUsesLocalOnlyWithoutEndpoint(t *testing.T) {
	sink := sinkFromConfig(Config{Enabled: true, Exporter: "otlp", Endpoint: ""})
	if _, ok := sink.(observeSink); !ok {
		t.Fatalf("sink = %T, want local observeSink", sink)
	}
}

func TestSinkFromConfigSelectsOTLPExporter(t *testing.T) {
	clearActiveExporter(t)
	sink := sinkFromConfig(Config{
		Enabled:  true,
		Exporter: "otlp",
		Endpoint: "http://127.0.0.1:4318",
		Protocol: ProtocolHTTPProtobuf,
	})
	if _, ok := sink.(*OTLPExporter); !ok {
		t.Fatalf("sink = %T, want *OTLPExporter", sink)
	}
	if current := currentActiveExporter(); current == nil {
		t.Fatal("exporter was not registered for shutdown")
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSinkFromConfigGRPCFallsBackLocal(t *testing.T) {
	clearActiveExporter(t)
	sink := sinkFromConfig(Config{
		Enabled:  true,
		Exporter: "otlp",
		Endpoint: "http://127.0.0.1:4317",
		Protocol: ProtocolGRPC,
	})
	if _, ok := sink.(observeSink); !ok {
		t.Fatalf("sink = %T, want local observeSink for grpc", sink)
	}
	if current := currentActiveExporter(); current != nil {
		t.Fatal("grpc installed an exporter in a stdlib-only build")
	}
}

func TestOTLPExporterGRPCReturnsExplicitError(t *testing.T) {
	exp, err := NewOTLPExporter(OTLPConfig{
		Endpoint: "http://127.0.0.1:4317",
		Protocol: ProtocolGRPC,
	})
	if err == nil {
		t.Fatal("grpc must return an explicit error, not a silent fallback")
	}
	if exp != nil {
		t.Fatalf("grpc returned an exporter %T, want nil", exp)
	}
	if !strings.Contains(err.Error(), "grpc") {
		t.Fatalf("error %q does not name the rejected protocol", err)
	}
}

func TestDisabledTracerInstallsNoExporter(t *testing.T) {
	clearActiveExporter(t)
	_ = NewTracer(Config{Enabled: false, Exporter: "otlp", Endpoint: "http://127.0.0.1:4318"})
	if current := currentActiveExporter(); current != nil {
		t.Fatal("disabled tracer installed an exporter")
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOTLPExporterCollectorDownDoesNotBlock(t *testing.T) {
	exp, err := NewOTLPExporter(OTLPConfig{
		Endpoint: "http://127.0.0.1:1",
		Protocol: ProtocolHTTPProtobuf,
		Timeout:  200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	exp.Emit(otlpTestSpan("beacon.dead.collector"))
	done := make(chan error, 1)
	go func() { done <- exp.Shutdown(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked on an unavailable collector")
	}
}

func clearActiveExporter(t *testing.T) {
	t.Helper()
	activeExporterMu.Lock()
	previous := activeExporter
	activeExporter = nil
	activeExporterMu.Unlock()
	t.Cleanup(func() {
		activeExporterMu.Lock()
		activeExporter = previous
		activeExporterMu.Unlock()
	})
}

func currentActiveExporter() Shutdowner {
	activeExporterMu.RLock()
	defer activeExporterMu.RUnlock()
	return activeExporter
}

var _ SpanSink = (*OTLPExporter)(nil)
