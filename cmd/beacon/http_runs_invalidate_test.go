package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// saveStatus stands in for the status service. save swaps the runs the next
// Active call returns, which models a reconciler save committing to the store.
type saveStatus struct {
	mu   sync.Mutex
	runs []*core.Run
}

func (s *saveStatus) Active(context.Context) ([]*core.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs, nil
}

func (s *saveStatus) save(runs []*core.Run) {
	s.mu.Lock()
	s.runs = runs
	s.mu.Unlock()
}

func savedRun(reason string) []*core.Run {
	return []*core.Run{{
		ID:        "r1",
		TaskID:    "T-1",
		State:     core.RunRunning,
		PRNumber:  7,
		HeadSHA:   "abc123",
		Branch:    "beacon/T-1",
		Reason:    reason,
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
}

func getRuns(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/runs status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestBroadcastThenSaveYieldsSavedRunState proves the failure from T-5115e3a5:
// a monitor that reads runs between a PR broadcast and the reconciler save must
// not keep the pre-save bytes after the save commits. The store content, not
// the event, decides the bytes the next GET returns.
func TestBroadcastThenSaveYieldsSavedRunState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status := &saveStatus{runs: savedRun("before save")}
	changes := newChangeSubscribers()
	src := make(chan int, 1)
	go changes.run(ctx, src)
	stream, unsubscribe := changes.subscribe()
	defer unsubscribe()

	api := &apiServer{
		services: apiServices{status: status},
		token:    []byte("secret"),
		frames:   newEventFrames(nil, json.Marshal),
		changes:  changes,
	}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	before := getRuns(t, srv)
	if !strings.Contains(before, "before save") {
		t.Fatalf("initial GET = %q, want the pre-save reason", before)
	}

	// The broadcast invalidates the cache and tells monitors to re-read.
	src <- 1
	select {
	case <-stream:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast frame did not reach the monitor")
	}

	// A monitor that reads now refills the cache with pre-save bytes.
	if got := getRuns(t, srv); !strings.Contains(got, "before save") {
		t.Fatalf("GET after broadcast = %q, want the pre-save reason", got)
	}

	// The reconciler commits the save after the broadcast. No new event fires.
	status.save(savedRun("after save"))

	after := getRuns(t, srv)
	if !strings.Contains(after, "after save") {
		t.Fatalf("GET after save = %q, want the saved reason", after)
	}
	if strings.Contains(after, "before save") {
		t.Fatalf("GET after save = %q, still holds the pre-save bytes", after)
	}
}

// TestHarnessEventThenSaveYieldsSavedRunState covers the same race on the
// harness path: invalidation from an idle event must not pin the cache to the
// bytes that were in the store before the reconciler saved.
func TestHarnessEventThenSaveYieldsSavedRunState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status := &saveStatus{runs: savedRun("before save")}
	// An unbuffered channel makes the send wait until invalidateOnHarness has
	// taken the event, so the GET below runs after the invalidation hint.
	events := make(chan core.HarnessEvent)
	changes := newChangeSubscribers()
	go invalidateOnHarness(ctx, events, changes.snapshot)

	api := &apiServer{
		services: apiServices{status: status},
		token:    []byte("secret"),
		frames:   newEventFrames(nil, json.Marshal),
		changes:  changes,
	}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	if got := getRuns(t, srv); !strings.Contains(got, "before save") {
		t.Fatalf("initial GET = %q, want the pre-save reason", got)
	}

	events <- core.HarnessEvent{RunID: "r1", Kind: "idle"}

	// A monitor that reads now still sees the bytes from before the save.
	if got := getRuns(t, srv); !strings.Contains(got, "before save") {
		t.Fatalf("GET after harness event = %q, want the pre-save reason", got)
	}

	status.save(savedRun("after save"))

	after := getRuns(t, srv)
	if !strings.Contains(after, "after save") || strings.Contains(after, "before save") {
		t.Fatalf("GET after save = %q, want only the saved reason", after)
	}
}
