package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/refusedguy/beacon/internal/domain"
)

func TestStatusForCode(t *testing.T) {
	tests := []struct {
		code   domain.ErrorCode
		status int
	}{
		{domain.CodeInvalidInput, 400},
		{domain.CodeWatchNotFound, 404},
		{domain.CodeHeadSHAChanged, 409},
		{domain.CodeRateLimited, 429},
		{domain.CodeLimitExceeded, 429},
		{domain.CodeCancelled, 410},
		{domain.CodeGone, 410},
		{domain.CodePersistFailed, 503},
	}
	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			if got := StatusForCode(tc.code); got != tc.status {
				t.Errorf("StatusForCode(%s) = %d, want %d", tc.code, got, tc.status)
			}
		})
	}
	if got := StatusForCode(domain.ErrorCode("UNKNOWN")); got != 0 {
		t.Errorf("StatusForCode(UNKNOWN) = %d, want 0", got)
	}
}

func TestToErrorShapeAllCodes(t *testing.T) {
	tests := []struct {
		code   domain.ErrorCode
		status int
	}{
		{domain.CodeInvalidInput, 400},
		{domain.CodeWatchNotFound, 404},
		{domain.CodeHeadSHAChanged, 409},
		{domain.CodeRateLimited, 429},
		{domain.CodeLimitExceeded, 429},
		{domain.CodeCancelled, 410},
		{domain.CodeGone, 410},
		{domain.CodePersistFailed, 503},
	}
	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			shape, ok := ToErrorShape(domain.NewAppError(tc.code, "boom"))
			if !ok {
				t.Fatal("ToErrorShape returned false for an AppError")
			}
			if shape.OK {
				t.Error("error shape must carry ok=false")
			}
			if shape.Code != tc.code {
				t.Errorf("code = %q, want %q", shape.Code, tc.code)
			}
			if shape.Status != tc.status {
				t.Errorf("status = %d, want %d", shape.Status, tc.status)
			}
			if shape.Message != "boom" {
				t.Errorf("message = %q, want %q", shape.Message, "boom")
			}
		})
	}
}

func TestToErrorShapeHeadSHAChangedCarriesNewWatchID(t *testing.T) {
	appErr := &domain.AppError{
		Code:       domain.CodeHeadSHAChanged,
		Message:    "head moved",
		Status:     409,
		NewWatchID: "watch-new",
	}
	shape, ok := ToErrorShape(appErr)
	if !ok {
		t.Fatal("ToErrorShape returned false for an AppError")
	}
	if shape.NewWatchID != "watch-new" {
		t.Errorf("newWatchID = %q, want %q", shape.NewWatchID, "watch-new")
	}
	if shape.Status != 409 {
		t.Errorf("status = %d, want 409", shape.Status)
	}
}

func TestToErrorShapeRateLimitedCarriesRetryAfterMs(t *testing.T) {
	retry := int64(1500)
	appErr := &domain.AppError{
		Code:         domain.CodeRateLimited,
		Message:      "secondary limit",
		Status:       429,
		RetryAfterMs: &retry,
	}
	shape, ok := ToErrorShape(appErr)
	if !ok {
		t.Fatal("ToErrorShape returned false for an AppError")
	}
	if shape.RetryAfterMs == nil || *shape.RetryAfterMs != retry {
		t.Errorf("retryAfterMs = %v, want %d", shape.RetryAfterMs, retry)
	}
	if shape.Status != 429 {
		t.Errorf("status = %d, want 429", shape.Status)
	}
}

func TestToErrorShapeStatusComesFromCodeTable(t *testing.T) {
	// The boundary table is authoritative even when an AppError carries a
	// stale status field.
	appErr := &domain.AppError{Code: domain.CodeGone, Message: "gone", Status: 500}
	shape, ok := ToErrorShape(appErr)
	if !ok {
		t.Fatal("ToErrorShape returned false for an AppError")
	}
	if shape.Status != 410 {
		t.Errorf("status = %d, want 410 from the code table", shape.Status)
	}
}

func TestToErrorShapeWrappedAndPlainErrors(t *testing.T) {
	wrapped := fmt.Errorf("tool boundary: %w", domain.NewAppError(domain.CodeInvalidInput, "bad input"))
	shape, ok := ToErrorShape(wrapped)
	if !ok || shape.Code != domain.CodeInvalidInput || shape.Status != 400 {
		t.Fatalf("wrapped AppError = %+v, ok=%v", shape, ok)
	}

	if _, ok := ToErrorShape(errors.New("plain")); ok {
		t.Error("plain error must not convert to an error shape")
	}
	if _, ok := ToErrorShape(nil); ok {
		t.Error("nil error must not convert to an error shape")
	}
}

func TestErrorShapeJSON(t *testing.T) {
	retry := int64(250)
	shape := ErrorShape{
		OK:           false,
		Code:         domain.CodeRateLimited,
		Message:      "slow down",
		Status:       429,
		RetryAfterMs: &retry,
		NewWatchID:   "watch-1",
	}
	data, err := json.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal error shape: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal error shape: %v", err)
	}
	want := []string{"ok", "code", "message", "status", "retryAfterMs", "newWatchID"}
	if len(fields) != len(want) {
		t.Errorf("field count = %d (%s), want %d", len(fields), string(data), len(want))
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing field %q in %s", key, data)
		}
	}
	if fields["ok"] != false {
		t.Errorf("ok = %v, want false", fields["ok"])
	}

	minimal, err := json.Marshal(ErrorShape{Code: domain.CodeGone, Message: "gone", Status: 410})
	if err != nil {
		t.Fatalf("marshal minimal error shape: %v", err)
	}
	var minimalFields map[string]any
	if err := json.Unmarshal(minimal, &minimalFields); err != nil {
		t.Fatalf("unmarshal minimal error shape: %v", err)
	}
	for _, key := range []string{"retryAfterMs", "newWatchID"} {
		if _, ok := minimalFields[key]; ok {
			t.Errorf("optional field %q must be omitted when empty: %s", key, minimal)
		}
	}
}

// jsonFieldNames reports the JSON property names of the exported fields of T.
func jsonFieldNames[T any]() map[string]bool {
	t := reflect.TypeFor[T]()
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" {
			name = t.Field(i).Name
		}
		if name != "-" {
			names[name] = true
		}
	}
	return names
}

// assertSchemaFromType proves the schema was reflected from T: its properties
// are exactly the JSON field names of T, and no literal JSON is involved.
func assertSchemaFromType[T any](t *testing.T, schema *jsonschema.Schema) {
	t.Helper()
	if schema == nil {
		t.Fatalf("nil schema for %T", *new(T))
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q, want object for %T", schema.Type, *new(T))
	}
	want := jsonFieldNames[T]()
	got := make(map[string]bool, len(schema.Properties))
	for name := range schema.Properties {
		got[name] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schema properties for %T = %v, want %v", *new(T), got, want)
	}
}

func TestAllOutputSchemas(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	wantTools := []string{
		"beacon_watch_pr",
		"beacon_wait_result",
		"beacon_get_result",
		"beacon_list_running",
		"beacon_ack",
		"beacon_cancel_watch",
		"beacon_stats",
	}
	if len(schemas) != len(wantTools) {
		t.Errorf("schema count = %d, want %d", len(schemas), len(wantTools))
	}
	for _, name := range wantTools {
		if _, ok := schemas[name]; !ok {
			t.Errorf("missing output schema for %s", name)
		}
	}

	assertSchemaFromType[WatchPROutput](t, schemas["beacon_watch_pr"])
	assertSchemaFromType[WaitResultOutput](t, schemas["beacon_wait_result"])
	assertSchemaFromType[GetResultOutput](t, schemas["beacon_get_result"])
	assertSchemaFromType[ListRunningOutput](t, schemas["beacon_list_running"])
	assertSchemaFromType[OKOutput](t, schemas["beacon_ack"])
	assertSchemaFromType[OKOutput](t, schemas["beacon_cancel_watch"])
	assertSchemaFromType[StatsOutput](t, schemas["beacon_stats"])
}

// TestWatchPRSchemaExecutorMetadata proves the notify-metadata executor is
// reflected into the watch_pr output schema while the original five fields
// keep their names and required status (T5.32, ADR 0094).
func TestWatchPRSchemaExecutorMetadata(t *testing.T) {
	schemas, err := AllOutputSchemas()
	if err != nil {
		t.Fatalf("AllOutputSchemas: %v", err)
	}
	schema := schemas["beacon_watch_pr"]
	if schema == nil {
		t.Fatal("missing watch_pr output schema")
	}

	original := []string{"watchID", "revision", "status", "verdict", "pollAfterMs"}
	for _, name := range original {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("watch_pr schema lost original field %q", name)
		}
	}
	executor, ok := schema.Properties["executor"]
	if !ok {
		t.Fatal("watch_pr schema missing executor")
	}

	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}
	for _, name := range original {
		if !required[name] {
			t.Errorf("original field %q is no longer required", name)
		}
	}
	if required["executor"] {
		t.Error("executor must be optional so a response without it stays valid")
	}
	if len(schema.Required) != len(original) {
		t.Errorf("required fields = %v, want exactly the original five", schema.Required)
	}

	for _, name := range []string{"id", "role", "runtime", "agent", "capabilities", "configId"} {
		if _, ok := executor.Properties[name]; !ok {
			t.Errorf("executor schema missing %q", name)
		}
	}
}

// TestWatchPROutputExecutorValidation drives the derived schema against two
// marshaled responses: one from a caller that omitted the descriptor and one
// that supplied it. Both must validate (T5.32 acceptance).
func TestWatchPROutputExecutorValidation(t *testing.T) {
	schema, err := OutputSchema[WatchPROutput]()
	if err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve watch_pr schema: %v", err)
	}

	base := WatchPROutput{
		WatchID:     "w-1",
		Revision:    1,
		Status:      "watching",
		Verdict:     "watching",
		PollAfterMs: 180000,
	}
	check := func(name string, out WatchPROutput, wantExecutor bool) {
		t.Helper()
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		var instance map[string]any
		if err := json.Unmarshal(raw, &instance); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		if err := resolved.Validate(instance); err != nil {
			t.Fatalf("%s does not validate against derived schema: %v", name, err)
		}
		_, got := instance["executor"]
		if got != wantExecutor {
			t.Errorf("%s executor presence = %v, want %v: %s", name, got, wantExecutor, raw)
		}
	}
	check("legacy response", base, false)

	withExecutor := base
	withExecutor.Executor = &ExecutorMetadata{
		ID:           "ses_1",
		Role:         "executor",
		Runtime:      "subagent",
		Agent:        "hermes",
		Capabilities: []string{"watch"},
		ConfigID:     "cfg-1",
	}
	check("extended response", withExecutor, true)
}

// TestGetResultSchemaDerivesTaskFields proves the dialect fields come from the
// Go output type, so the resultType discriminator is part of the advertised
// schema and can never drift from the response struct (ADR 0003).
func TestGetResultSchemaDerivesTaskFields(t *testing.T) {
	schema, err := OutputSchema[GetResultOutput]()
	if err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	for _, name := range []string{"resultType", "taskId", "taskStatus", "pollIntervalMs", "jobID", "instructions"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("get_result schema missing dialect property %q", name)
		}
	}
}

// TestApplyTaskFormIsVersionBlind proves the selector only takes a dialect,
// a duration and an --after threshold; no protocol version reaches it.
func TestApplyTaskFormIsVersionBlind(t *testing.T) {
	base := GetResultOutput{Status: "notifying", ObligationID: "job-1"}
	long := 2 * time.Second
	after := time.Second

	plain := ApplyTaskForm(base, DialectPlain, long, after)
	if plain.JobID != "job-1" || plain.Instructions == "" || plain.ResultType != "" {
		t.Errorf("plain long call = %+v", plain)
	}
	modern := ApplyTaskForm(base, DialectTasks2026, long, after)
	if modern.ResultType != TaskResultType || modern.TaskID != "job-1" {
		t.Errorf("2026 long call = %+v", modern)
	}
	legacy := ApplyTaskForm(base, DialectTasks2025, 0, after)
	if legacy.TaskID != "job-1" || legacy.ResultType != "" {
		t.Errorf("2025 immediate task = %+v", legacy)
	}
	if quick := ApplyTaskForm(base, DialectTasks2026, 0, after); quick.ResultType != "" || quick.TaskID != "" {
		t.Errorf("2026 quick call must be classic: %+v", quick)
	}
	if disabled := ApplyTaskForm(base, DialectPlain, time.Hour, 0); disabled.JobID != "" {
		t.Errorf("zero --after must disable the switch: %+v", disabled)
	}
}

func TestOutputSchemaHonorsJSONTags(t *testing.T) {
	schema, err := OutputSchema[WaitResultOutput]()
	if err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}
	for _, name := range []string{"status", "revision"} {
		if !required[name] {
			t.Errorf("%q must be a required property (no omitempty)", name)
		}
	}
	for _, name := range []string{"verdict", "checks", "pollAfterMs"} {
		if required[name] {
			t.Errorf("%q must be optional (omitempty)", name)
		}
	}
	for _, name := range []string{"status", "verdict", "revision", "checks", "pollAfterMs"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("missing derived property %q", name)
		}
	}
}

// cursorSnapshotReader serves a snapshot that a test can replace between
// calls, so a second call can observe events that appeared after the first.
type cursorSnapshotReader struct {
	snap StateSnapshot
}

func (f *cursorSnapshotReader) Snapshot() (StateSnapshot, error) { return f.snap, nil }

func callListRunningCursor(t *testing.T, handler *ListRunningHandler, in ListRunningInput) ListRunningOutput {
	t.Helper()
	result, out, err := handler.Handle(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("beacon_list_running: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("beacon_list_running returned an error result: %#v", result)
	}
	return out
}

// TestListRunningOutputSchemaHasStatusAndCursor proves the MCP response schema
// carries status and cursor, plus the event and terminal-verdict fields ADR
// 0025 requires.
func TestListRunningOutputSchemaHasStatusAndCursor(t *testing.T) {
	schema, err := OutputSchema[ListRunningOutput]()
	if err != nil {
		t.Fatalf("OutputSchema: %v", err)
	}
	for _, name := range []string{"status", "cursor", "events", "verdict"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("list_running output schema missing property %q", name)
		}
	}
}

// TestListRunningCursorReturnsOnlyNewEvents is the two-sequential-calls check
// from ADR 0025: the first call returns every event and a cursor; the second
// call, given that cursor, returns only events that appeared in between.
func TestListRunningCursorReturnsOnlyNewEvents(t *testing.T) {
	reader := &cursorSnapshotReader{snap: StateSnapshot{ActiveWatches: []domain.Watch{
		{ID: "w1", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1},
	}}}
	handler := NewListRunningHandler(reader)

	first := callListRunningCursor(t, handler, ListRunningInput{})
	if first.Cursor == "" {
		t.Fatal("first response must carry a non-empty cursor")
	}
	if first.Status != string(domain.StatusWatching) {
		t.Errorf("first status = %q, want watching", first.Status)
	}
	if len(first.Events) != 1 || first.Events[0].WatchID != "w1" {
		t.Fatalf("first events = %+v, want only w1", first.Events)
	}

	reader.snap = StateSnapshot{ActiveWatches: []domain.Watch{
		{ID: "w1", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1},
		{ID: "w2", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 2},
	}}
	second := callListRunningCursor(t, handler, ListRunningInput{Cursor: first.Cursor})

	if len(second.Events) != 1 {
		t.Fatalf("second events = %+v, want exactly one new event", second.Events)
	}
	if second.Events[0].WatchID != "w2" {
		t.Errorf("new event watchID = %q, want w2", second.Events[0].WatchID)
	}
	for _, ev := range second.Events {
		if ev.WatchID == "w1" {
			t.Error("second call replayed old event w1")
		}
	}
}

// TestListRunningTerminalVerdictWithoutOldEvents proves that once a watch is
// terminal, a cursor call returns the verdict and status without replaying the
// earlier events (ADR 0025 acceptance).
func TestListRunningTerminalVerdictWithoutOldEvents(t *testing.T) {
	reader := &cursorSnapshotReader{snap: StateSnapshot{ActiveWatches: []domain.Watch{
		{ID: "w1", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1},
	}}}
	handler := NewListRunningHandler(reader)
	first := callListRunningCursor(t, handler, ListRunningInput{})

	reader.snap = StateSnapshot{ActiveWatches: []domain.Watch{
		{ID: "w1", Status: domain.StatusWatching, Verdict: domain.VerdictWatching, Revision: 1},
		{ID: "w2", Status: domain.StatusClosed, Verdict: domain.VerdictMerged, Revision: 2},
	}}
	second := callListRunningCursor(t, handler, ListRunningInput{Cursor: first.Cursor})

	if second.Status != string(domain.StatusClosed) {
		t.Errorf("terminal status = %q, want closed", second.Status)
	}
	if second.Verdict != string(domain.VerdictMerged) {
		t.Errorf("terminal verdict = %q, want merged", second.Verdict)
	}
	if len(second.Events) != 0 {
		t.Errorf("terminal response replayed old events: %+v", second.Events)
	}
}

// TestListRunningRejectsMalformedCursor keeps the opaque cursor a strict input:
// a client cannot pass arbitrary bytes and silently get a full replay.
func TestListRunningRejectsMalformedCursor(t *testing.T) {
	handler := NewListRunningHandler(&cursorSnapshotReader{snap: sampleSnapshot()})
	_, _, err := handler.Handle(context.Background(), nil, ListRunningInput{Cursor: "!!!not-a-cursor!!!"})
	if err == nil {
		t.Fatal("malformed cursor must be rejected")
	}
}
