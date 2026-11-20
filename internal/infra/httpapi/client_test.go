package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDoer struct {
	fn func(*http.Request) (*http.Response, error)
}

func (f fakeDoer) Do(req *http.Request) (*http.Response, error) { return f.fn(req) }

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testConnection() ClientConn {
	return ClientConn{Host: "127.0.0.1", Port: 8787, Token: "sekret"}
}

func TestLoadConnectionReadsEndpoint(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"host":"localhost","port":9123,"token":"t"}`)
	if err := os.WriteFile(filepath.Join(dir, clientConnectionFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := LoadClientConn(dir)
	if err != nil {
		t.Fatalf("LoadClientConn: %v", err)
	}
	if conn.Host != "localhost" || conn.Port != 9123 || conn.Token != "t" {
		t.Fatalf("connection = %+v", conn)
	}
	if got := conn.Address(); got != "localhost:9123" {
		t.Fatalf("address = %q", got)
	}
}

func TestLoadConnectionMissingIsTyped(t *testing.T) {
	_, err := LoadClientConn(t.TempDir())
	if !errors.Is(err, ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection", err)
	}
	if !strings.Contains(err.Error(), clientConnectionFileName) {
		t.Fatalf("err = %v, want the file name", err)
	}
}

func TestLoadConnectionRejectsInvalidRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, clientConnectionFileName), []byte(`{"host":"","port":80}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientConn(dir); err == nil {
		t.Fatal("empty host must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, clientConnectionFileName), []byte(`{"host":"h","port":70000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientConn(dir); err == nil {
		t.Fatal("out-of-range port must fail")
	}
}

func TestClientSnapshotUsesBearerAndProjections(t *testing.T) {
	conn := testConnection()
	var gotMethod, gotPath, gotAuth string
	client, err := NewProjectionClient(conn, fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		gotMethod, gotPath = req.Method, req.URL.Path
		gotAuth = req.Header.Get("Authorization")
		body := `{
			"project":"beacon",
			"points":{"pointsThisHour":42,"budget":500},
			"cycle":{"cycleID":"cycle-9","durationMs":1500},
			"orchestrator":{"live":true,"sessionID":"ses-orch"},
			"watches":[{"prNumber":"#407","watchID":"w-1","verdict":"ready","status":"watching","blockerID":"b-2","subscriberSession":"ses-1","phase":"executor","fix_attempts":1,"wake_attempts":3,"updated":"12:00"}],
			"attention":[{"message":"approval needed"}],
			"escalations":["base red"]
		}`
		return fakeResponse(http.StatusOK, body), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("ClientSnapshot: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != clientSnapshotPath {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if snap.Project != "beacon" || snap.Points.Used != 42 || snap.Points.Limit != 500 {
		t.Fatalf("header = %+v", snap)
	}
	if snap.Cycle.ID != "cycle-9" || snap.Cycle.Duration != "1.5s" {
		t.Fatalf("cycle = %+v", snap.Cycle)
	}
	if !snap.Orchestrator.Live || snap.Orchestrator.Session != "ses-orch" {
		t.Fatalf("orchestrator = %+v", snap.Orchestrator)
	}
	if len(snap.Watches) != 1 {
		t.Fatalf("watches = %+v", snap.Watches)
	}
	w := snap.Watches[0]
	if w.PR != "#407" || w.Watch != "w-1" || w.Blocker != "b-2" || w.Subscriber != "ses-1" {
		t.Fatalf("watch = %+v", w)
	}
	if w.FixAttempts != 1 || w.WakeAttempts != 3 || w.Phase != "executor" {
		t.Fatalf("attempts/phase = %+v", w)
	}
	if len(snap.Attention) != 1 || snap.Attention[0] != "approval needed" {
		t.Fatalf("attention = %+v", snap.Attention)
	}
	if len(snap.Escalations) != 1 || snap.Escalations[0] != "base red" {
		t.Fatalf("escalations = %+v", snap.Escalations)
	}
}

func TestClientSnapshotReportsHTTPError(t *testing.T) {
	client, err := NewProjectionClient(testConnection(), fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusServiceUnavailable, "not ready"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientDecidePostsDecisionWithBearer(t *testing.T) {
	conn := testConnection()
	var gotMethod, gotPath, gotAuth, gotBody string
	client, err := NewProjectionClient(conn, fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		gotMethod, gotPath, gotAuth = req.Method, req.URL.Path, req.Header.Get("Authorization")
		body, _ := io.ReadAll(req.Body)
		gotBody = string(body)
		return fakeResponse(http.StatusOK, `{"ok":true,"applied":true}`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Decide(context.Background(), ClientDecision{Verb: "approve", Target: "w-1", Note: "looks good"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !receipt.OK || !receipt.Applied {
		t.Fatalf("receipt = %+v", receipt)
	}
	if gotMethod != http.MethodPost || gotPath != clientDecidePath || gotAuth != "Bearer sekret" {
		t.Fatalf("request = %s %s %q", gotMethod, gotPath, gotAuth)
	}
	var decoded ClientDecision
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("body %q: %v", gotBody, err)
	}
	if decoded.Verb != "approve" || decoded.Target != "w-1" || decoded.Note != "looks good" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestClientDecideRefusesNegativeReceipt(t *testing.T) {
	client, err := NewProjectionClient(testConnection(), fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusOK, `{"ok":false,"message":"not parked"}`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Decide(context.Background(), ClientDecision{Verb: "approve", Target: "w-1"}); err == nil || !strings.Contains(err.Error(), "not parked") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientTransportErrorsAreTyped(t *testing.T) {
	client, err := NewProjectionClient(testConnection(), fakeDoer{fn: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Snapshot(context.Background()); !errors.Is(err, ErrNoServer) {
		t.Fatalf("snapshot err = %v, want ErrNoServer", err)
	}
	if _, err := client.Decide(context.Background(), ClientDecision{Verb: "cancel", Target: "w-1"}); !errors.Is(err, ErrNoServer) {
		t.Fatalf("decide err = %v, want ErrNoServer", err)
	}
}

func TestClientEventsParsesFramesAndSendsLastEventID(t *testing.T) {
	var seenLastID string
	calls := 0
	client, err := NewProjectionClient(testConnection(), fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			seenLastID = req.Header.Get("Last-Event-ID")
			body := "id: 1\nevent: watch\ndata: {\"watch\":\"w-1\"}\n\nid: 2\nevent: verdict\ndata: {\"verdict\":\"ready\"}\n\n"
			return fakeResponse(http.StatusOK, body), nil
		}
		seenLastID = req.Header.Get("Last-Event-ID")
		return fakeResponse(http.StatusOK, ""), nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := client.Events(ctx)
	got := make([]ClientEvent, 0, 2)
	for result := range results {
		if result.Err != nil {
			t.Fatalf("event error: %v", result.Err)
		}
		got = append(got, result.Event)
		if len(got) == 2 {
			cancel()
		}
	}
	if len(got) != 2 {
		t.Fatalf("events = %+v", got)
	}
	if got[0].ID != "1" || got[0].Type != "watch" || !strings.Contains(string(got[0].Data), "w-1") {
		t.Fatalf("first event = %+v", got[0])
	}
	if got[1].ID != "2" || got[1].Type != "verdict" {
		t.Fatalf("second event = %+v", got[1])
	}
	if seenLastID != "" {
		t.Fatalf("first subscription sent Last-Event-ID %q", seenLastID)
	}

	// A resubscribe carries the last id so the server can resume the stream.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	for range client.Events(ctx2) {
		cancel2()
	}
	if seenLastID != "2" {
		t.Fatalf("Last-Event-ID = %q, want 2", seenLastID)
	}
}

func TestNewValidatesConnection(t *testing.T) {
	if _, err := NewProjectionClient(ClientConn{Port: 80}, nil); err == nil {
		t.Fatal("empty host must fail")
	}
}
