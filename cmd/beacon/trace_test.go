package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/store"
)

type fakeTraceStore struct {
	events []store.JournalEvent
	err    error
}

func (f *fakeTraceStore) LoadEvents() ([]store.JournalEvent, []store.LoadError, error) {
	return f.events, nil, f.err
}

var _ traceEventStore = (*fakeTraceStore)(nil)

func withTraceDeps(t *testing.T, store traceEventStore) {
	t.Helper()
	previous := makeTraceDeps
	makeTraceDeps = func(*CLI) (traceDeps, func() error, error) {
		return traceDeps{Events: store}, func() error { return nil }, nil
	}
	t.Cleanup(func() { makeTraceDeps = previous })
}

func withTraceClock(t *testing.T, at time.Time) {
	t.Helper()
	previous := traceNow
	traceNow = func() time.Time { return at }
	t.Cleanup(func() { traceNow = previous })
}

func traceFixture(watchID, kind, reason, source string, revision int64, at time.Time) store.JournalEvent {
	payload := fmt.Sprintf(`{"kind":%q,"message":"message stays out of reason","reason":%q,"source":%q,"revision":%d,"watchID":%q}`,
		kind, reason, source, revision, watchID)
	return store.JournalEvent{
		WatchID:       watchID,
		Type:          kind,
		SchemaVersion: 4,
		Payload:       json.RawMessage(payload),
		CreatedAt:     at.UnixNano(),
	}
}

func runTraceWith(t *testing.T, events []store.JournalEvent, args ...string) (int, string, string) {
	t.Helper()
	withTraceDeps(t, &fakeTraceStore{events: events})
	var out, errOut bytes.Buffer
	code := runTrace(&CLI{out: &out, errOut: &errOut}, args)
	return code, out.String(), errOut.String()
}

func TestTraceReportsWakeAckRetryWithStoredEvidence(t *testing.T) {
	fixed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	withTraceClock(t, fixed)
	wakeAt := fixed.Add(-time.Minute)
	ackAt := wakeAt.Add(time.Second)
	retryAt := ackAt.Add(time.Second)

	events := []store.JournalEvent{
		traceFixture("ses_test", "wake", "keep_working", "beacon_ack", 3, wakeAt),
		traceFixture("ses_test", "ack", "failed check", "orchestrator", 4, ackAt),
		traceFixture("ses_test", "retry", "gate red", "ci", 5, retryAt),
		traceFixture("ses_other", "wake", "foreign reason", "peer", 1, wakeAt),
		traceFixture("ses_test", "created", "ignored reason", "peer", 1, wakeAt),
		traceFixture("ses_test", "wake", "too old", "peer", 1, fixed.Add(-time.Hour)),
	}

	code, out, errOut := runTraceWith(t, events, "--session", "ses_test", "--since", "10m")
	if code != 0 {
		t.Fatalf("trace code = %d, stderr: %q", code, errOut)
	}

	want := []string{
		"at=" + wakeAt.UTC().Format(time.RFC3339Nano) + " watchID=ses_test kind=wake revision=3 reason=\"keep_working\" source=\"beacon_ack\"",
		"at=" + ackAt.UTC().Format(time.RFC3339Nano) + " watchID=ses_test kind=ack revision=4 reason=\"failed check\" source=\"orchestrator\"",
		"at=" + retryAt.UTC().Format(time.RFC3339Nano) + " watchID=ses_test kind=retry revision=5 reason=\"gate red\" source=\"ci\"",
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(want) {
		t.Fatalf("trace lines = %d, want %d: %q", len(lines), len(want), out)
	}
	for i, line := range lines {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
		for _, field := range []string{"at=", "watchID=", "revision=", "reason="} {
			if !strings.Contains(line, field) {
				t.Errorf("line %d misses %s: %q", i, field, line)
			}
		}
	}
	for _, unwanted := range []string{"foreign reason", "ignored reason", "too old", "message stays out of reason"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("вывод содержит %q: %q", unwanted, out)
		}
	}
}

func TestTraceUnknownSessionPrintsNoEvidence(t *testing.T) {
	fixed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	withTraceClock(t, fixed)
	events := []store.JournalEvent{
		traceFixture("ses_other", "wake", "real reason", "peer", 1, fixed.Add(-time.Minute)),
	}

	code, out, errOut := runTraceWith(t, events, "--session", "ses_test", "--since", "10m")
	if code != 0 {
		t.Fatalf("trace code = %d, stderr: %q", code, errOut)
	}
	if strings.TrimSpace(out) != "no evidence" {
		t.Fatalf("out = %q, want no evidence", out)
	}
}

func TestTraceMissingReasonIsNotInvented(t *testing.T) {
	fixed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	withTraceClock(t, fixed)
	ev := traceFixture("ses_test", "ack", "", "", 2, fixed.Add(-time.Minute))
	ev.Payload = json.RawMessage(`{"kind":"ack","message":"no reason in this row","revision":2}`)

	code, out, errOut := runTraceWith(t, []store.JournalEvent{ev}, "--session", "ses_test", "--since", "10m")
	if code != 0 {
		t.Fatalf("trace code = %d, stderr: %q", code, errOut)
	}
	if !strings.Contains(out, `reason="" source=""`) {
		t.Fatalf("отсутствующие поля должны быть пустыми, без выдуманного значения: %q", out)
	}
	if strings.Contains(out, "no reason in this row") {
		t.Fatalf("message подменён как reason: %q", out)
	}
}

func TestTraceJSONFormatCarriesTheSameFields(t *testing.T) {
	fixed := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	withTraceClock(t, fixed)
	at := fixed.Add(-time.Minute)
	events := []store.JournalEvent{
		traceFixture("ses_test", "wake", "keep_working", "beacon_ack", 7, at),
	}

	code, out, errOut := runTraceWith(t, events, "--session", "ses_test", "--since", "10m", "--format", "json")
	if code != 0 {
		t.Fatalf("trace code = %d, stderr: %q", code, errOut)
	}
	var records []traceRecord
	if err := json.Unmarshal([]byte(out), &records); err != nil {
		t.Fatalf("json decode: %v; out: %q", err, out)
	}
	want := traceRecord{
		At:       at.UTC().Format(time.RFC3339Nano),
		WatchID:  "ses_test",
		Kind:     "wake",
		Revision: 7,
		Reason:   "keep_working",
		Source:   "beacon_ack",
	}
	if len(records) != 1 || records[0] != want {
		t.Fatalf("records = %+v, want [%+v]", records, want)
	}

	code, out, errOut = runTraceWith(t, events, "--session", "ses_test", "--since", "10m", "--json")
	if code != 0 || strings.TrimSpace(out) == "" || strings.Contains(errOut, "неизвестный формат") {
		t.Fatalf("alias --json: code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestTraceRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--session", " "},
		{"--session", "ses_test", "--since", "oops"},
		{"--session", "ses_test", "--since", "0m"},
		{"--session", "ses_test", "--format", "yaml"},
		{"--session", "ses_test", "extra"},
	} {
		code, _, _ := runTraceWith(t, nil, args...)
		if code != 2 {
			t.Errorf("args %v: code = %d, want 2", args, code)
		}
	}
}

func TestTraceStoreErrorFailsClosed(t *testing.T) {
	withTraceDeps(t, &fakeTraceStore{err: fmt.Errorf("store down")})
	var out, errOut bytes.Buffer
	if code := runTrace(&CLI{out: &out, errOut: &errOut}, []string{"--session", "ses_test"}); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "store down") {
		t.Fatalf("ошибка store не доставлена: %q", errOut.String())
	}
}

func TestTraceIsCataloguedAndDispatchable(t *testing.T) {
	var found *command
	for i := range catalog {
		if catalog[i].Name == "trace" {
			found = &catalog[i]
			break
		}
	}
	if found == nil {
		t.Fatal("trace отсутствует в каталоге команд")
	}
	for _, flag := range []string{"--session", "--since", "--format table|json"} {
		if !strings.Contains(found.Usage, flag) {
			t.Errorf("usage без %s: %q", flag, found.Usage)
		}
	}
	if found.Summary == "" {
		t.Error("у trace пустое описание")
	}
	if runnerFor["trace"] == nil {
		t.Error("trace есть в каталоге, но отсутствует в диспетчере")
	}
	formats := commandFormats["trace"]
	if strings.Join(formats, ",") != "table,json" {
		t.Errorf("commandFormats[trace] = %v, want table,json", formats)
	}
}
