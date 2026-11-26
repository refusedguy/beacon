package main

import (
	"bufio"
	"context"
	"encoding/json"
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

	api := &apiServer{token: []byte("secret"), changes: subs, frames: newEventFrames(nil, json.Marshal)}
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

// TestHarnessEventEncodedOnceForAllMonitors proves that a burst of harness
// events costs one JSON encode per event, not one per monitor. Eight subscribers
// see byte-identical frames and the marshaller reports no errors. Lossy
// channels may drop frames under the burst, but every delivered frame must
// match the pre-encoded bytes.
func TestHarnessEventEncodedOnceForAllMonitors(t *testing.T) {
	const (
		monitors = 8
		total    = 5000
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan core.HarnessEvent)
	var mu sync.Mutex
	encodes, marshalErrors := 0, 0
	marshal := func(v any) ([]byte, error) {
		b, err := json.Marshal(v)
		mu.Lock()
		encodes++
		if err != nil {
			marshalErrors++
		}
		mu.Unlock()
		return b, err
	}

	frames := newEventFrames(src, marshal)
	runDone := make(chan struct{})
	go func() {
		frames.run(ctx)
		close(runDone)
	}()

	subs := make([]<-chan []byte, monitors)
	for i := range subs {
		subs[i], _ = frames.subscribe()
	}

	// The lossy channels may drop frames under a tight burst, so readers drain
	// until the source ends and then pick up anything still buffered.
	stop := make(chan struct{})
	got := make([][][]byte, monitors)
	var wg sync.WaitGroup
	for i := range subs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case frame := <-subs[i]:
					got[i] = append(got[i], frame)
				case <-stop:
					for {
						select {
						case frame := <-subs[i]:
							got[i] = append(got[i], frame)
						default:
							return
						}
					}
				}
			}
		}(i)
	}

	ev := core.HarnessEvent{RunID: "r1", Kind: "progress"}
	for j := 0; j < total; j++ {
		src <- ev
	}
	close(src)
	<-runDone
	close(stop)
	wg.Wait()

	mu.Lock()
	encodeCount, errorCount := encodes, marshalErrors
	mu.Unlock()
	if encodeCount != total {
		t.Fatalf("marshaller encodes = %d, want %d (one per event)", encodeCount, total)
	}
	if errorCount != 0 {
		t.Fatalf("marshaller errors = %d, want 0", errorCount)
	}

	recorder := httptest.NewRecorder()
	writeSSE(recorder, "harness", core.NewWireEvent(ev))
	want := recorder.Body.String()
	delivered := 0
	for i, received := range got {
		if len(received) == 0 {
			t.Fatalf("monitor %d received no frames", i)
		}
		for j, frame := range received {
			if string(frame) != want {
				t.Fatalf("monitor %d frame %d = %q, want %q", i, j, frame, want)
			}
			delivered++
		}
	}
	t.Logf("%d events to %d monitors: %d encodes, %d errors, %d identical frames delivered (%d bytes each)",
		total, monitors, encodeCount, errorCount, delivered, len(want))
}

// TestEventFramesDropsSlowSubscriber makes the lossy monitor policy explicit.
// A monitor that never reads loses the overflow while a fast monitor sees
// every frame.
func TestEventFramesDropsSlowSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan core.HarnessEvent)
	frames := newEventFrames(src, json.Marshal)
	go frames.run(ctx)

	slow, unSlow := frames.subscribe()
	defer unSlow()
	fast, unFast := frames.subscribe()
	defer unFast()

	total := subscriberBuffer + 1
	for j := 0; j < total; j++ {
		src <- core.HarnessEvent{Kind: "tick"}
		select {
		case <-fast:
		case <-time.After(2 * time.Second):
			t.Fatalf("fast monitor stalled at frame %d", j)
		}
	}

	count := 0
	for {
		select {
		case <-slow:
			count++
		default:
			if count != subscriberBuffer {
				t.Fatalf("slow monitor buffered %d frames, want %d", count, subscriberBuffer)
			}
			return
		}
	}
}

// TestHarnessFramesReachEveryMonitorOverHTTP proves the pre-encoded frames are
// written to each monitor with the unchanged notification wire shape.
func TestHarnessFramesReachEveryMonitorOverHTTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan core.HarnessEvent)
	frames := newEventFrames(src, json.Marshal)
	go frames.run(ctx)

	api := &apiServer{token: []byte("secret"), frames: frames, changes: newChangeSubscribers()}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	const monitors = 8
	streams := make([]*bufio.Scanner, monitors)
	closers := make([]func(), monitors)
	for i := range streams {
		streams[i], closers[i] = connectSSE(t, srv, "secret")
	}
	defer func() {
		for _, close := range closers {
			close()
		}
	}()

	ev := core.HarnessEvent{RunID: "r1", Kind: "done"}
	recorder := httptest.NewRecorder()
	writeSSE(recorder, "harness", core.NewWireEvent(ev))
	lines := strings.Split(recorder.Body.String(), "\n")
	if len(lines) < 2 || lines[0] != "event: harness" {
		t.Fatalf("unexpected wire frame %q", recorder.Body.String())
	}
	want := strings.TrimPrefix(lines[1], "data: ")

	for rep := 0; rep < 2; rep++ {
		src <- ev
		for i, stream := range streams {
			if got := sseData(t, stream, "harness"); got != want {
				t.Fatalf("repetition %d monitor %d data = %q, want %q", rep, i, got, want)
			}
		}
	}
	t.Logf("harness frame over HTTP: %q", recorder.Body.String())
}

// TestCombinedHarnessAndPRChangeReachOneMonitor drives a harness event and a
// PR change from their sources in one tick and asserts one connected monitor
// receives both frames with the documented notification shapes. The harness
// frame reaches the monitor through frames.run; the PR frame reaches it through
// changeSubscribers. Arriving together must not drop either frame.
func TestCombinedHarnessAndPRChangeReachOneMonitor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mirror serve and startAPIServer wiring: one harness view feeds frames.run
	// while a second ends the snapshot generation, and one PR stream feeds the
	// change broadcaster. Both sources reach the same SSE handler.
	harnessSrc := make(chan core.HarnessEvent)
	changeSrc := make(chan int)
	views := fanout(ctx, harnessSrc, 2)
	subs := newChangeSubscribers()
	go invalidateOnHarness(ctx, views[1], subs.snapshot)
	frames := newEventFrames(views[0], json.Marshal)
	go frames.run(ctx)
	go subs.run(ctx, changeSrc)

	api := &apiServer{token: []byte("secret"), frames: frames, changes: subs}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	stream, closeStream := connectSSE(t, srv, "secret")
	defer closeStream()

	ev := core.HarnessEvent{RunID: "r-combined", Kind: "progress"}
	const pr = 73

	// Both expected frames go through the documented SSE writer, so the data
	// lines below are the wire shape a monitor is promised.
	expect := func(event string, payload any) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		writeSSE(recorder, event, payload)
		lines := strings.Split(recorder.Body.String(), "\n")
		if len(lines) < 2 || lines[0] != "event: "+event {
			t.Fatalf("unexpected wire frame %q", recorder.Body.String())
		}
		return strings.TrimPrefix(lines[1], "data: ")
	}
	wantHarness := expect("harness", core.NewWireEvent(ev))
	wantPR := expect("pr_changed", map[string]int{"pr": pr})

	// Drive both sources before reading either frame. The SSE handler may write
	// them in either order, so collect by event name instead of reading in a
	// fixed sequence.
	harnessSrc <- ev
	changeSrc <- pr

	got := make(map[string]string, 2)
	for len(got) < 2 && stream.Scan() {
		line := stream.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		name := strings.TrimPrefix(line, "event: ")
		if name != "harness" && name != "pr_changed" {
			continue
		}
		if !stream.Scan() {
			t.Fatalf("SSE stream closed before %s data: %v", name, stream.Err())
		}
		got[name] = strings.TrimPrefix(stream.Text(), "data: ")
	}
	if len(got) != 2 {
		t.Fatalf("SSE stream closed before both frames: got %v, err %v", got, stream.Err())
	}
	if got["harness"] != wantHarness {
		t.Fatalf("harness data = %q, want %q", got["harness"], wantHarness)
	}
	if got["pr_changed"] != wantPR {
		t.Fatalf("pr_changed data = %q, want %q", got["pr_changed"], wantPR)
	}
	t.Logf("combined stream delivered harness %q and pr_changed %q", got["harness"], got["pr_changed"])
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
		token:    []byte("secret"),
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

func TestAuthAcceptsOnlyTheExactToken(t *testing.T) {
	services := &recordingAPIServices{}
	srv := newRecordingAPIServer(t, services)

	for _, tc := range []struct {
		name   string
		header string
		want   int
	}{
		{"exact", "Bearer secret", http.StatusOK},
		{"wrong", "Bearer wrong", http.StatusUnauthorized},
		{"longer", "Bearer secretx", http.StatusUnauthorized},
		{"shorter", "Bearer secre", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"no scheme", "secret", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", tc.header)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			t.Logf("Authorization %q -> %d", tc.header, resp.StatusCode)
		})
	}

	// The one accepted request reaches Active; every rejected request stops at
	// the HTTP boundary.
	if calls := services.recorded(); len(calls) != 1 || calls[0] != "active" {
		t.Fatalf("service calls = %v, want [active] from the accepted request only", calls)
	}
}

// TestAuthDoesNotAllocatePerRequest proves the token bytes are stored once at
// construction and the header is viewed, not copied, on each request.
func TestAuthDoesNotAllocatePerRequest(t *testing.T) {
	const iterations = 100

	api := &apiServer{token: []byte("secret")}
	passed := 0
	handler := api.auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		passed++
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	allocs := testing.AllocsPerRun(iterations, func() {
		handler.ServeHTTP(rec, req)
	})
	if allocs != 0 {
		t.Fatalf("auth allocations per request = %v, want 0", allocs)
	}
	// AllocsPerRun calls f once to warm up before the measured iterations.
	if passed != iterations+1 {
		t.Fatalf("requests passed to the next handler = %d, want %d", passed, iterations+1)
	}
}

// countingStatus returns a fixed run list and counts every Active call. It
// stands in for the status service so a test can observe store reads.
type countingStatus struct {
	mu    sync.Mutex
	calls int
	runs  []*core.Run
}

func (s *countingStatus) Active(context.Context) ([]*core.Run, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.runs, nil
}

func (s *countingStatus) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestRunsSnapshotSharedAcrossMonitorsPerGeneration proves that eight monitors
// reacting to one PR change share a single store read. The whole scenario runs
// twenty times; one Active call per generation means twenty calls in total.
func TestRunsSnapshotSharedAcrossMonitorsPerGeneration(t *testing.T) {
	const (
		monitors    = 8
		repetitions = 20
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan int, 1)
	subs := newChangeSubscribers()
	go subs.run(ctx, src)

	runs := []*core.Run{{
		ID:        "r1",
		TaskID:    "T-1",
		State:     core.RunRunning,
		PRNumber:  7,
		HeadSHA:   "abc123",
		Branch:    "beacon/T-1",
		Reason:    "queued",
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	status := &countingStatus{runs: runs}
	api := &apiServer{
		services: apiServices{status: status},
		token:    []byte("secret"),
		frames:   newEventFrames(nil, json.Marshal),
		changes:  subs,
	}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	streams := make([]*bufio.Scanner, monitors)
	closers := make([]func(), monitors)
	for i := range streams {
		streams[i], closers[i] = connectSSE(t, srv, "secret")
	}
	defer func() {
		for _, close := range closers {
			close()
		}
	}()

	// The cached bytes must match what writeJSON produced before the cache.
	recorder := httptest.NewRecorder()
	writeJSON(recorder, core.NewWireRuns(runs))
	want := recorder.Body.String()

	for rep := 0; rep < repetitions; rep++ {
		src <- rep
		for i, stream := range streams {
			data := sseData(t, stream, "pr_changed")
			if wantData := fmt.Sprintf(`"pr":%d`, rep); !strings.Contains(data, wantData) {
				t.Fatalf("repetition %d monitor %d got %q, want %s", rep, i, data, wantData)
			}
		}

		bodies := make([]string, monitors)
		var wg sync.WaitGroup
		for i := range streams {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs", nil)
				if err != nil {
					t.Error(err)
					return
				}
				req.Header.Set("Authorization", "Bearer secret")
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Error(err)
					return
				}
				bodies[i] = string(body)
			}(i)
		}
		wg.Wait()

		for i, body := range bodies {
			if body != want {
				t.Fatalf("repetition %d monitor %d body = %q, want byte-identical %q", rep, i, body, want)
			}
		}
		if got := status.count(); got != rep+1 {
			t.Fatalf("after %d generations Active calls = %d, want %d", rep+1, got, rep+1)
		}
	}
}

// TestHarnessEventInvalidatesRunsSnapshot proves that a harness change ends the
// current generation even with no monitor attached to the event stream.
func TestHarnessEventInvalidatesRunsSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status := &countingStatus{}
	cache := &runsCache{}
	load := func() string {
		t.Helper()
		body, err := cache.snapshot(context.Background(), status)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	if first, second := load(), load(); first != second {
		t.Fatalf("reads in one generation differ: %q vs %q", first, second)
	}
	if got := status.count(); got != 1 {
		t.Fatalf("Active calls before harness change = %d, want 1", got)
	}

	events := make(chan core.HarnessEvent, 1)
	done := make(chan struct{})
	go func() {
		invalidateOnHarness(ctx, events, cache)
		close(done)
	}()
	events <- core.HarnessEvent{}
	close(events)
	<-done

	_ = load()
	if got := status.count(); got != 2 {
		t.Fatalf("Active calls after harness change = %d, want 2", got)
	}
}

// TestRunHTTPShutsDownOnContextCancel proves the shared server lifecycle used
// by the loopback API and the webhook. Cancelling ctx shuts the server down and
// reports http.ErrServerClosed, which the serve command treats as a clean exit.
func TestRunHTTPShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	runHTTP(ctx, errCh, "127.0.0.1:0", http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	}))
	cancel()
	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			t.Fatalf("runHTTP exit = %v, want %v", err, http.ErrServerClosed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runHTTP did not shut down after ctx was cancelled")
	}
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
