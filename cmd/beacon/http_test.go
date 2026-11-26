package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// connectSSE opens a monitor stream and returns its frame scanner. The handler
// flushes the response headers on connect, so Do returns before any event.
func connectSSE(t *testing.T, srv *httptest.Server, token string) (*bufio.Scanner, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}
	return bufio.NewScanner(resp.Body), func() {
		cancel()
		resp.Body.Close()
	}
}

// sseData reads frames until an event with the given name and returns its data.
func sseData(t *testing.T, stream *bufio.Scanner, name string) string {
	t.Helper()
	for stream.Scan() {
		if stream.Text() != "event: "+name {
			continue
		}
		if !stream.Scan() {
			t.Fatalf("SSE stream closed before %s data: %v", name, stream.Err())
		}
		return strings.TrimPrefix(stream.Text(), "data: ")
	}
	t.Fatalf("SSE stream closed before %s event: %v", name, stream.Err())
	return ""
}

func TestPRChangeReachesReconcilerAndEveryMonitor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan int, 1)
	views := fanout(ctx, src, 2)
	subs := newChangeSubscribers()
	go subs.run(ctx, views[1])

	// The reconciler reads the first view. Stand in for it so the test can
	// observe the wake-up without standing up a full reconciler.
	woken := make(chan int, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case pr := <-views[0]:
				woken <- pr
			}
		}
	}()

	api := &apiServer{token: "secret", changes: subs}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	first, closeFirst := connectSSE(t, srv, "secret")
	defer closeFirst()
	second, closeSecond := connectSSE(t, srv, "secret")
	defer closeSecond()

	src <- 42

	select {
	case pr := <-woken:
		if pr != 42 {
			t.Fatalf("reconciler wake-up = %d, want 42", pr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not wake on the PR change")
	}

	for name, stream := range map[string]*bufio.Scanner{"first": first, "second": second} {
		data := sseData(t, stream, "pr_changed")
		if !strings.Contains(data, `"pr":42`) {
			t.Fatalf("%s monitor got %q, want pr 42", name, data)
		}
	}
}

func TestChangeSubscribersDropsSlowClientWithoutBlockingOthers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan int)
	subs := newChangeSubscribers()
	slow, unSlow := subs.subscribe()
	defer unSlow()
	fast, unFast := subs.subscribe()
	defer unFast()

	go subs.run(ctx, src)

	// Never drain slow. Broadcast more changes than its buffer holds.
	total := subscriberBuffer + 1
	for i := 0; i < total; i++ {
		src <- i
		select {
		case got := <-fast:
			if got != i {
				t.Fatalf("fast subscriber got %d, want %d", got, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("fast subscriber stalled at change %d", i)
		}
	}

	// slow kept the first subscriberBuffer changes and dropped the last one.
	count := 0
	for {
		select {
		case <-slow:
			count++
		default:
			if count != subscriberBuffer {
				t.Fatalf("slow subscriber buffered %d changes, want %d", count, subscriberBuffer)
			}
			return
		}
	}
}

// recordingAPIServices records every call that reaches the service layer. A
// request rejected at the HTTP boundary must leave the record empty.
type recordingAPIServices struct {
	mu    sync.Mutex
	calls []string
}

func (s *recordingAPIServices) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *recordingAPIServices) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *recordingAPIServices) Active(context.Context) ([]*core.Run, error) {
	s.record("active")
	return nil, nil
}

func (s *recordingAPIServices) WatchPR(_ context.Context, id core.RunID, pr int) error {
	s.record(fmt.Sprintf("watch %s %d", id, pr))
	return nil
}

func (s *recordingAPIServices) Approve(_ context.Context, id core.RunID, sha string) error {
	s.record("approve " + string(id) + " " + sha)
	return nil
}

func (s *recordingAPIServices) Unblock(_ context.Context, id core.RunID) error {
	s.record("unblock " + string(id))
	return nil
}

func (s *recordingAPIServices) Cancel(_ context.Context, id core.RunID) error {
	s.record("cancel " + string(id))
	return nil
}

func (s *recordingAPIServices) Retry(_ context.Context, id core.RunID) error {
	s.record("retry " + string(id))
	return nil
}

func (s *recordingAPIServices) Nudge(_ context.Context, id core.RunID, message string) error {
	s.record("nudge " + string(id) + " " + message)
	return nil
}

func newRecordingAPIServer(t *testing.T, services *recordingAPIServices) *httptest.Server {
	t.Helper()
	api := &apiServer{
		services: apiServices{status: services, control: services},
		token:    "secret",
		changes:  newChangeSubscribers(),
	}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPRoutesRejectMissingBearerToken(t *testing.T) {
	services := &recordingAPIServices{}
	srv := newRecordingAPIServer(t, services)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/runs"},
		{http.MethodGet, "/api/v1/events"},
		{http.MethodPost, "/api/v1/control"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path,
				strings.NewReader(`{"action":"cancel","run_id":"r1"}`))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if got := strings.TrimSpace(string(body)); got != "unauthorized" {
				t.Fatalf("body = %q, want %q", got, "unauthorized")
			}
			t.Logf("%s %s without token -> %d %q", tc.method, tc.path, resp.StatusCode, strings.TrimSpace(string(body)))
		})
	}
	if calls := services.recorded(); len(calls) != 0 {
		t.Fatalf("service calls after rejected requests = %v, want none", calls)
	}
	t.Logf("service calls after three rejected requests: %d", len(services.recorded()))
}

func TestHTTPControlAcceptsKnownActionAndRejectsUnknownAction(t *testing.T) {
	services := &recordingAPIServices{}
	srv := newRecordingAPIServer(t, services)

	post := func(t *testing.T, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/control", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		reply, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, strings.TrimSpace(string(reply))
	}

	const known = `{"action":"cancel","run_id":"r1"}`
	status, body := post(t, known)
	if status != http.StatusOK || body != `{"ok":true,"action":"cancel","run_id":"r1","applied":true}` {
		t.Fatalf("known action -> %d %s, want 200 and the control result", status, body)
	}
	if calls := services.recorded(); len(calls) != 1 || calls[0] != "cancel r1" {
		t.Fatalf("service calls = %v, want [cancel r1]", calls)
	}
	t.Logf("POST /api/v1/control %s with token -> %d %s", known, status, body)

	const unknown = `{"action":"teleport","run_id":"r1"}`
	status, body = post(t, unknown)
	if status != http.StatusBadRequest || body != "unknown action" {
		t.Fatalf("unknown action -> %d %q, want 400 and %q", status, body, "unknown action")
	}
	if calls := services.recorded(); len(calls) != 1 {
		t.Fatalf("service calls after unknown action = %v, want [cancel r1] only", calls)
	}
	t.Logf("POST /api/v1/control %s with token -> %d %q, service calls still %d",
		unknown, status, body, len(services.recorded()))
}
