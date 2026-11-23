package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
