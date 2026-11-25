package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunsEnvelope(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[{"id":"run-1","task_id":"T1","state":"running","pr":12,"head_sha":"abc","updated_at":"2026-11-23T10:00:00Z"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runs, err := c.Runs(ctx)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].TaskID != "T1" || runs[0].PR != 12 {
		t.Fatalf("unexpected runs: %+v", runs)
	}
	if gotPath != apiRunsPath || gotMethod != http.MethodGet {
		t.Fatalf("request: %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization: %q", gotAuth)
	}
}

func TestRunsArrayAndOptionalToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"id":"run-2","state":"blocked"}]`))
	}))
	defer srv.Close()

	runs, err := NewClient(srv.URL, "").Runs(context.Background())
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 1 || runs[0].State != "blocked" {
		t.Fatalf("unexpected runs: %+v", runs)
	}
	if gotAuth != "" {
		t.Fatalf("unexpected authorization: %q", gotAuth)
	}
}

func TestControl(t *testing.T) {
	var gotPath, gotAuth, gotType string
	var got ControlRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"run_id":"run-1","action":"approve","applied":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	result, err := c.Control(context.Background(), ControlRequest{Action: ActionApprove, RunID: "run-1", SHA: "abc"})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	if !result.OK || !result.Applied || result.RunID != "run-1" || result.Action != "approve" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotPath != apiControlPath || gotAuth != "Bearer tok" || gotType != "application/json" {
		t.Fatalf("request: path=%s auth=%q type=%q", gotPath, gotAuth, gotType)
	}
	if got.Action != ActionApprove || got.RunID != "run-1" || got.SHA != "abc" {
		t.Fatalf("unexpected body: %+v", got)
	}
}

func TestClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, "").Runs(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected status error, got %v", err)
	}
	if _, err := NewClient(srv.URL, "").Control(context.Background(), ControlRequest{Action: ActionRetry}); err == nil {
		t.Fatal("expected missing run id error")
	}
	for _, endpoint := range []string{"http://example.com:7432", "http://192.168.1.2:7432", "not-a-url"} {
		if _, err := NewClient(endpoint, "").Runs(context.Background()); err == nil {
			t.Fatalf("expected loopback error for %q", endpoint)
		}
	}
}

// TestLoopbackWireDTO drives the client against a handler that emits the
// daemon's documented wire shapes. Every field the monitor reads must survive
// the round trip.
func TestLoopbackWireDTO(t *testing.T) {
	var runsBody, controlBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiRunsPath:
			w.Header().Set("Content-Type", "application/json")
			body, err := json.Marshal(map[string]any{"runs": []Run{{
				ID:        "run-9",
				TaskID:    "T-9",
				State:     "waiting_ci",
				PR:        42,
				HeadSHA:   "deadbeef",
				Branch:    "beacon/T-9",
				Reason:    "checking CI",
				UpdatedAt: "2026-11-25T10:00:00Z",
			}}})
			if err != nil {
				t.Errorf("marshal runs: %v", err)
				return
			}
			runsBody = string(body)
			_, _ = w.Write(body)
		case apiControlPath:
			w.Header().Set("Content-Type", "application/json")
			body, err := json.Marshal(ControlResult{OK: true, Action: "approve", RunID: "run-9", Applied: true})
			if err != nil {
				t.Errorf("marshal control result: %v", err)
				return
			}
			controlBody = string(body)
			_, _ = w.Write(body)
		case apiEventsPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: harness\ndata: {\"id\":17,\"type\":\"idle\",\"run_id\":\"run-9\",\"at\":\"2026-11-25T10:00:00Z\"}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	runs, err := c.Runs(context.Background())
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	wantRun := Run{
		ID:        "run-9",
		TaskID:    "T-9",
		State:     "waiting_ci",
		PR:        42,
		HeadSHA:   "deadbeef",
		Branch:    "beacon/T-9",
		Reason:    "checking CI",
		UpdatedAt: "2026-11-25T10:00:00Z",
	}
	if !reflect.DeepEqual(runs, []Run{wantRun}) {
		t.Fatalf("runs = %+v, want %+v", runs, wantRun)
	}
	for _, key := range []string{`"id":`, `"task_id":`, `"state":`, `"pr":`, `"head_sha":`, `"branch":`, `"reason":`, `"updated_at":`} {
		if !strings.Contains(runsBody, key) {
			t.Errorf("runs body %q is missing %s", runsBody, key)
		}
	}

	result, err := c.Control(context.Background(), ControlRequest{Action: ActionApprove, RunID: "run-9"})
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	wantResult := ControlResult{OK: true, Action: "approve", RunID: "run-9", Applied: true}
	if result != wantResult {
		t.Fatalf("control result = %+v, want %+v", result, wantResult)
	}
	for _, key := range []string{`"ok":`, `"action":`, `"run_id":`, `"applied":`} {
		if !strings.Contains(controlBody, key) {
			t.Errorf("control body %q is missing %s", controlBody, key)
		}
	}

	events := make(chan Event, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Stream(ctx, func(ev Event) {
			select {
			case events <- ev:
			default:
			}
		})
	}()
	select {
	case ev := <-events:
		wantEvent := Event{ID: 17, Type: "idle", RunID: "run-9", At: "2026-11-25T10:00:00Z"}
		if ev != wantEvent {
			t.Fatalf("event = %+v, want %+v", ev, wantEvent)
		}
	case <-ctx.Done():
		t.Fatal("no event received")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Stream: %v", err)
	}
}
