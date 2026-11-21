package otel

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

func fixedTime() time.Time {
	return time.Date(2026, time.November, 20, 12, 0, 0, 0, time.UTC)
}

func testRecord(name string) Record {
	return Record{
		Time:       fixedTime(),
		Name:       name,
		TraceID:    "00112233445566778899aabbccddeeff",
		SpanID:     "0011223344556677",
		DurationUS: 1500,
		Attrs:      map[string]string{"beacon.component": "test"},
	}
}

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	sink, err := NewSink(path)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	want := testRecord("beacon.local.write")
	if err := sink.Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sink.Path() != path {
		t.Fatalf("Path = %q, want %q", sink.Path(), path)
	}
	if err := sink.LastError(); err != nil {
		t.Fatalf("LastError = %v, want nil", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read returned %d records, want 1", len(got))
	}
	if got[0].Name != want.Name || got[0].TraceID != want.TraceID ||
		got[0].SpanID != want.SpanID || got[0].DurationUS != want.DurationUS {
		t.Fatalf("record mismatch: got %+v, want %+v", got[0], want)
	}
	if got[0].Attrs["beacon.component"] != "test" {
		t.Fatalf("attrs = %v, want beacon.component=test", got[0].Attrs)
	}
	if !got[0].Time.Equal(want.Time) {
		t.Fatalf("time = %v, want %v", got[0].Time, want.Time)
	}
}

func TestEmitFromSpan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	sink, err := NewSink(path)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	span := observe.Span{
		Name:  "beacon.order.merge",
		Ctx:   observe.SpanContext{TraceID: [16]byte{1, 2, 3}, SpanID: [8]byte{4, 5, 6}, Flags: observe.TraceFlagSampled},
		Start: fixedTime(),
		End:   fixedTime().Add(3 * time.Millisecond),
		Attrs: []observe.Attr{observe.String("beacon.watch_id", "watch:1")},
	}
	span.ParentSpanID = [8]byte{7, 8, 9}
	sink.Emit(span)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read returned %d records, want 1", len(got))
	}
	r := got[0]
	if r.Name != span.Name || r.TraceID != "01020300000000000000000000000000" ||
		r.SpanID != "0405060000000000" || r.ParentSpanID != "0708090000000000" {
		t.Fatalf("unexpected converted record: %+v", r)
	}
	if r.DurationUS != 3000 {
		t.Fatalf("DurationUS = %d, want 3000", r.DurationUS)
	}
	if r.Attrs["beacon.watch_id"] != "watch:1" {
		t.Fatalf("attrs = %v, want beacon.watch_id", r.Attrs)
	}
}

func TestEmitWithoutParentOmitsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)
	sink, err := NewSink(path)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	sink.Emit(observe.Span{Name: "root", Start: fixedTime(), End: fixedTime()})
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].ParentSpanID != "" {
		t.Fatalf("unexpected record: %+v", got)
	}
}

func TestRotationBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	sink, err := NewSink(path, WithMaxBytes(1))
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	var want []string
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("span.%d", i)
		want = append(want, name)
		if err := sink.Write(testRecord(name)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadAll returned %d records, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("record %d = %q, want %q", i, got[i].Name, name)
		}
	}
	if len(archivePaths(path)) == 0 {
		t.Fatal("size rotation produced no archive")
	}
}

func TestRotationByAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	now := fixedTime()
	sink, err := NewSink(path, WithClock(func() time.Time { return now }), WithMaxAge(time.Hour))
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	if err := sink.Write(testRecord("before")); err != nil {
		t.Fatalf("Write before: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if err := sink.Write(testRecord("after")); err != nil {
		t.Fatalf("Write after: %v", err)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 2 || got[0].Name != "before" || got[1].Name != "after" {
		t.Fatalf("unexpected history: %+v", got)
	}
}

func TestPruneMaxFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	sink, err := NewSink(path, WithMaxBytes(1), WithMaxFiles(2))
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	for i := 1; i <= 5; i++ {
		if err := sink.Write(testRecord(fmt.Sprintf("span.%d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if n := len(archivePaths(path)); n != 2 {
		t.Fatalf("kept %d archives, want 2", n)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := []string{"span.3", "span.4", "span.5"}
	if len(got) != len(want) {
		t.Fatalf("ReadAll returned %d records, want %d: %+v", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("record %d = %q, want %q", i, got[i].Name, name)
		}
	}
}

func TestDecodeSkipsBlankLinesAndReportsBadLine(t *testing.T) {
	good, err := json.Marshal(testRecord("ok"))
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	input := string(good) + "\n\n" + "{not json}\n"
	got, err := Decode(strings.NewReader(input))
	if err == nil {
		t.Fatal("Decode accepted malformed NDJSON")
	}
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("Decode returned %+v, want one good record", got)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error = %q, want line 3", err)
	}
}

func TestInstallWritesWithoutExport(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv(observe.EnabledEnv, "false")
	t.Setenv(observe.ExporterEnv, "")

	dir := t.TempDir()
	restore, err := Install(dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	defer restore()

	ctx, end := observe.Current().Start(context.Background(), "beacon.local.sink", observe.String("source", "local"))
	if _, ok := observe.SpanContextFromContext(ctx); !ok {
		t.Fatal("local tracer did not install a span context")
	}
	end()

	got, err := ReadStateDir(dir)
	if err != nil {
		t.Fatalf("ReadStateDir: %v", err)
	}
	if len(got) != 1 || got[0].Name != "beacon.local.sink" {
		t.Fatalf("unexpected local history: %+v", got)
	}
	if got[0].Attrs["source"] != "local" {
		t.Fatalf("attrs = %v, want source=local", got[0].Attrs)
	}
}

func TestReadAllMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.ndjson")
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadAll returned %d records for a missing file", len(got))
	}
}

func wakeSpan(obligationID string) observe.Span {
	return observe.Span{
		Name:  wakeDeliverSpanName,
		Ctx:   observe.SpanContext{TraceID: [16]byte{1}, SpanID: [8]byte{2}},
		Start: fixedTime(),
		End:   fixedTime().Add(time.Millisecond),
		Attrs: []observe.Attr{
			observe.String("watchID", "watch:1"),
			observe.String(spanAttrObligationID, obligationID),
		},
	}
}

func TestWakeSpanUsesGenAIConventions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	sink, err := NewSink(path)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	span := wakeSpan("watch:1:7:ses_abc:1")
	span.Attrs = append(span.Attrs,
		observe.String("gen_ai.input.messages", "secret wake prompt"),
		observe.String("gen_ai.tool.call.arguments", "secret tool arguments"),
	)
	sink.Emit(span)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read returned %d records, want 1", len(got))
	}
	r := got[0]
	if r.Name != "execute_tool wake.opencode" {
		t.Fatalf("Name = %q, want %q", r.Name, "execute_tool wake.opencode")
	}
	wantAttrs := map[string]string{
		AttrOperationName: "execute_tool",
		AttrProviderName:  "opencode",
		AttrToolName:      "wake.opencode",
		AttrToolCallID:    "watch:1:7:ses_abc:1",
		AttrToolType:      "extension",
	}
	for key, want := range wantAttrs {
		if r.Attrs[key] != want {
			t.Errorf("attrs[%q] = %q, want %q", key, r.Attrs[key], want)
		}
	}
	if r.Attrs["watchID"] != "watch:1" || r.Attrs[spanAttrObligationID] != "watch:1:7:ses_abc:1" {
		t.Errorf("identifier attrs were lost: %v", r.Attrs)
	}
	for _, key := range []string{"gen_ai.input.messages", "gen_ai.tool.call.arguments"} {
		if _, ok := r.Attrs[key]; ok {
			t.Errorf("content attr %q reached the export: %v", key, r.Attrs)
		}
	}
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(line), "secret") {
		t.Fatalf("message content reached the export: %s", line)
	}
}

func TestWakeSpansKeepSeparateCallIDsAndNoTokenSum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)

	sink, err := NewSink(path)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	defer sink.Close()

	for _, id := range []string{"obligation:1", "obligation:2"} {
		sink.Emit(wakeSpan(id))
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Read returned %d records, want 2", len(got))
	}
	for i, want := range []string{"obligation:1", "obligation:2"} {
		if got[i].Attrs[AttrToolCallID] != want {
			t.Errorf("record %d tool.call.id = %q, want %q", i, got[i].Attrs[AttrToolCallID], want)
		}
		for key := range got[i].Attrs {
			if strings.Contains(key, "token") || strings.Contains(key, "usage") {
				t.Errorf("record %d carries token attribute %q", i, key)
			}
		}
	}
}
