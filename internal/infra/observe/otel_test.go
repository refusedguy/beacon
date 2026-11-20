package observe

import (
	"context"
	"encoding/hex"
	"testing"
	"time"
)

func TestConfigFromEnvDefaultsOff(t *testing.T) {
	t.Setenv(EnabledEnv, "")
	t.Setenv(ExporterEnv, "")
	cfg := ConfigFromEnv()
	if cfg.Enabled {
		t.Fatal("empty flag enabled tracing")
	}
	if cfg.Exporter != "" {
		t.Fatalf("exporter = %q, want empty", cfg.Exporter)
	}
}

func TestConfigFromEnvFlagAndExporter(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		enabled bool
	}{
		{"true", "true", true},
		{"mixed case", "TRUE", true},
		{"padded", " true ", true},
		{"one is not true", "1", false},
		{"garbage", "yes", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnabledEnv, tt.flag)
			t.Setenv(ExporterEnv, "otlp")
			cfg := ConfigFromEnv()
			if cfg.Enabled != tt.enabled {
				t.Fatalf("Enabled = %v, want %v", cfg.Enabled, tt.enabled)
			}
			if cfg.Exporter != "otlp" {
				t.Fatalf("Exporter = %q, want otlp", cfg.Exporter)
			}
		})
	}
}

func TestNoopTracerKeepsContextAndEmitsNothing(t *testing.T) {
	rec := &SpanRecorder{}
	tracer := NewTracer(Config{Enabled: false, SpanSink: rec})
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "sentinel")

	child, end := tracer.Start(ctx, "beacon.noop", String("beacon.watch_id", "w1"))
	if child != ctx {
		t.Fatal("disabled tracer changed the context")
	}
	if _, ok := SpanContextFromContext(child); ok {
		t.Fatal("disabled tracer installed a span context")
	}
	end()
	end() // idempotent even on the no-op path
	if got := rec.Spans(); len(got) != 0 {
		t.Fatalf("no-op emitted %d spans", len(got))
	}
	if got := child.Value(key{}); got != "sentinel" {
		t.Fatalf("lost context value: %v", got)
	}
}

func TestEnabledTracerRecordsAttrsAndEndsOnce(t *testing.T) {
	rec := &SpanRecorder{}
	tracer := NewTracer(Config{Enabled: true, SpanSink: rec})

	ctx, end := tracer.Start(context.Background(), "beacon.daemon.tick",
		String("beacon.component", "daemon"), String("process.executable.name", "beacon"))
	sc, ok := SpanContextFromContext(ctx)
	if !ok {
		t.Fatal("enabled tracer did not install a span context")
	}
	if sc.Flags&TraceFlagSampled == 0 {
		t.Fatal("span context is not sampled")
	}
	if len(sc.TraceParent()) != 55 {
		t.Fatalf("traceparent = %q, want 55 chars", sc.TraceParent())
	}
	end()
	end()
	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.Name != "beacon.daemon.tick" {
		t.Fatalf("name = %q", span.Name)
	}
	if span.Ctx != sc {
		t.Fatal("recorded span context differs from the one in the child context")
	}
	if len(span.Attrs) != 2 || span.Attrs[0] != String("beacon.component", "daemon") ||
		span.Attrs[1] != String("process.executable.name", "beacon") {
		t.Fatalf("attrs = %+v", span.Attrs)
	}
	if span.Duration() < 0 || span.End.Before(span.Start) {
		t.Fatalf("bad span times: %+v", span)
	}
}

func TestEnabledTracerNestsChildrenUnderSameTrace(t *testing.T) {
	rec := &SpanRecorder{}
	tracer := NewTracer(Config{Enabled: true, SpanSink: rec})

	rootCtx, endRoot := tracer.Start(context.Background(), "beacon.orchestrator.pass")
	rootSC, ok := SpanContextFromContext(rootCtx)
	if !ok {
		t.Fatal("root context has no span")
	}
	childCtx, endChild := tracer.Start(rootCtx, "beacon.wake.deliver", String("beacon.adapter", "T202"))
	if childSC, ok := SpanContextFromContext(childCtx); !ok || childSC.SpanID == rootSC.SpanID {
		t.Fatal("child context does not carry a new span ID")
	}
	endChild()
	endRoot()

	spans := rec.Spans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	child, root := spans[0], spans[1]
	if child.Ctx.TraceID != root.Ctx.TraceID {
		t.Fatal("child and root have different trace IDs")
	}
	if child.Ctx.SpanID == root.Ctx.SpanID {
		t.Fatal("child reused the root span ID")
	}
	if child.ParentSpanID != root.Ctx.SpanID {
		t.Fatalf("parent = %x, want %x", child.ParentSpanID, root.Ctx.SpanID)
	}
	if root.ParentSpanID != ([8]byte{}) {
		t.Fatal("root span has a parent")
	}
	if hex.EncodeToString(child.Ctx.SpanID[:]) != child.Ctx.TraceParent()[36:52] {
		t.Fatalf("traceparent does not carry the span ID: %q", child.Ctx.TraceParent())
	}
}

func TestEnabledTracerWithNilSinkDoesNotPanic(t *testing.T) {
	tracer := NewTracer(Config{Enabled: true})
	ctx, end := tracer.Start(nil, "beacon.ci.refresh")
	if _, ok := SpanContextFromContext(ctx); !ok {
		t.Fatal("no span context with the default sink")
	}
	end()
}

func TestSpanContextFromContextRejectsInvalid(t *testing.T) {
	if _, ok := SpanContextFromContext(nil); ok {
		t.Fatal("nil context produced a span context")
	}
	if _, ok := SpanContextFromContext(context.Background()); ok {
		t.Fatal("bare context produced a span context")
	}
	if (SpanContext{}).IsValid() {
		t.Fatal("zero span context is valid")
	}
	if got := (SpanContext{}).TraceParent(); got != "" {
		t.Fatalf("zero traceparent = %q", got)
	}
}

func TestSetTracerRestoresPrevious(t *testing.T) {
	previous := Current()
	rec := &SpanRecorder{}
	restore := SetTracer(NewTracer(Config{Enabled: true, SpanSink: rec}))
	_, end := Current().Start(context.Background(), "beacon.custom")
	end()
	if len(rec.Spans()) != 1 {
		t.Fatalf("custom tracer emitted %d spans", len(rec.Spans()))
	}
	restore()
	if Current() != previous {
		t.Fatal("restore did not put the previous tracer back")
	}
}

func TestRecorderSpansIsACopy(t *testing.T) {
	rec := &SpanRecorder{}
	tracer := NewTracer(Config{Enabled: true, SpanSink: rec})
	_, end := tracer.Start(context.Background(), "beacon.copy")
	end()
	spans := rec.Spans()
	spans[0].Name = "changed"
	if got := rec.Spans(); got[0].Name != "beacon.copy" {
		t.Fatalf("SpanRecorder.Spans exposed internal state: %q", got[0].Name)
	}
}

func TestEnabledTracerRecordsOrderedTimes(t *testing.T) {
	rec := &SpanRecorder{}
	tracer := NewTracer(Config{Enabled: true, SpanSink: rec})
	start := time.Now()
	_, end := tracer.Start(context.Background(), "beacon.span")
	end()
	span := rec.Spans()[0]
	if span.Start.Before(start) || span.End.Before(span.Start) {
		t.Fatalf("span times out of order: start=%v end=%v", span.Start, span.End)
	}
}
