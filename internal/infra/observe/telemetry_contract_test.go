package observe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type contractSink struct {
	logs []Fields
	incs int
}

func (s *contractSink) Log(_ Level, _ string, fields Fields) {
	s.logs = append(s.logs, fields)
}

func (s *contractSink) Inc(_, _ string) { s.incs++ }

func installContractSink(t *testing.T) *contractSink {
	t.Helper()
	sink := &contractSink{}
	previous := Default()
	SetDefault(sink)
	t.Cleanup(func() { SetDefault(previous) })
	return sink
}

var sensitiveFragments = []string{"/mnt/", "PATH=", "cannot open file"}

func assertNoSensitiveText(t *testing.T, got string) {
	t.Helper()
	for _, fragment := range sensitiveFragments {
		if strings.Contains(got, fragment) {
			t.Fatalf("sensitive fragment %q found in %q", fragment, got)
		}
	}
}

func TestCheckTelemetryDropsSensitiveValues(t *testing.T) {
	secret := "/mnt/projects/beacon-handoff PATH=topsecret cannot open file"
	fields := CheckTelemetry("otel", "local_fallback", secret, errors.New(secret))
	assertNoSensitiveText(t, fmt.Sprint(fields))
	if fields[TagCheckOutcome] != "error" {
		t.Fatalf("outcome = %v, want error", fields[TagCheckOutcome])
	}
	if _, ok := fields[TagCheckReason]; ok {
		t.Fatalf("reason was kept: %v", fields[TagCheckReason])
	}
}

func TestSerializedMetricsDropSensitiveAttrs(t *testing.T) {
	start := time.Unix(1, 0)
	span := Span{
		Name:  "beacon.check.run",
		Ctx:   SpanContext{TraceID: [16]byte{1}, SpanID: [8]byte{2}, Flags: TraceFlagSampled},
		Start: start,
		End:   start.Add(time.Millisecond),
		Attrs: []Attr{
			String("beacon.component", "checker"),
			String("beacon.watch_id", "/mnt/projects/beacon-handoff/secret"),
			String("beacon.adapter", "PATH=supersecret"),
			String("hook.phase", "cannot open file /mnt/secret"),
		},
	}
	for _, protocol := range []string{ProtocolHTTPJSON, ProtocolHTTPProtobuf} {
		body, _, err := encodeOTLPBatch([]Span{span}, protocol)
		if err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		assertNoSensitiveText(t, string(body))
		if protocol == ProtocolHTTPJSON && !strings.Contains(string(body), `"key":"beacon.component"`) {
			t.Fatalf("bounded attr was dropped: %s", body)
		}
	}
}

func TestFallbackCheckRunsOnceWithoutExporter(t *testing.T) {
	sink := installContractSink(t)
	logOTLPFallback("no-exporter", errors.New("dial /mnt/x PATH=secret cannot open file"))
	if len(sink.logs) != 1 {
		t.Fatalf("got %d fallback checks, want 1", len(sink.logs))
	}
	assertNoSensitiveText(t, fmt.Sprint(sink.logs[0]))
	if sink.logs[0][TagCheckOutcome] != "error" {
		t.Fatalf("outcome = %v, want error", sink.logs[0][TagCheckOutcome])
	}
	if sink.logs[0][TagCheckReason] != "no-exporter" {
		t.Fatalf("reason = %v, want no-exporter", sink.logs[0][TagCheckReason])
	}
}

func TestTracerEmitsOnceWithoutExporter(t *testing.T) {
	sink := installContractSink(t)
	tracer := NewTracer(Config{Enabled: true, Exporter: "otlp"})
	if _, ok := tracer.(*traceTracer); !ok {
		t.Fatal("absent exporter disabled tracing")
	}
	_, end := tracer.Start(context.Background(), "beacon.check.run")
	end()
	if len(sink.logs) != 1 {
		t.Fatalf("got %d span records without exporter, want 1", len(sink.logs))
	}
}
