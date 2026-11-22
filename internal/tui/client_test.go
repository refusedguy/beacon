package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type seenRequest struct {
	method     string
	path       string
	auth       string
	contentTyp string
	lastID     string
	body       string
}

func testClient(t *testing.T, handler http.Handler) (*Client, <-chan seenRequest) {
	t.Helper()
	seen := make(chan seenRequest, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body = make([]byte, 0, 512)
			buf := make([]byte, 512)
			for {
				n, err := r.Body.Read(buf)
				body = append(body, buf[:n]...)
				if err != nil {
					break
				}
			}
		}
		seen <- seenRequest{
			method:     r.Method,
			path:       r.URL.Path,
			auth:       r.Header.Get("Authorization"),
			contentTyp: r.Header.Get("Content-Type"),
			lastID:     r.Header.Get("Last-Event-ID"),
			body:       string(body),
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	client, err := NewClient(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, seen
}

func nextRequest(t *testing.T, seen <-chan seenRequest) seenRequest {
	t.Helper()
	select {
	case req := <-seen:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("server received no request")
		return seenRequest{}
	}
}

func TestSnapshotReadsProjection(t *testing.T) {
	client, seen := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"liveWatches":[{"id":"w1","pr":42,"headSHA":"abcdef1234","status":"watching","verdict":"waiting","revision":7,"subscribers":[{"id":"ses_x","phase":"executor"}]}],"pointStats":{"used":12,"limit":500},"escalations":["e1"]}`)
	}))

	snap, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.LiveWatches) != 1 {
		t.Fatalf("live watches = %d, want 1", len(snap.LiveWatches))
	}
	watch := snap.LiveWatches[0]
	if watch.PR != 42 || watch.Revision != 7 || watch.Verdict != "waiting" {
		t.Errorf("watch = %+v, want pr=42 rev=7 verdict=waiting", watch)
	}
	if len(watch.Subscribers) != 1 || watch.Subscribers[0].Phase != "executor" {
		t.Errorf("subscribers = %+v, want one executor", watch.Subscribers)
	}
	if snap.PointStats.Used != 12 || snap.PointStats.Limit != 500 {
		t.Errorf("points = %+v, want 12/500", snap.PointStats)
	}
	if len(snap.Escalations) != 1 || snap.Escalations[0] != "e1" {
		t.Errorf("escalations = %v, want [e1]", snap.Escalations)
	}
	req := nextRequest(t, seen)
	if req.method != http.MethodGet || req.path != snapshotPath || req.auth != "Bearer secret" {
		t.Errorf("request = %+v, want GET %s with bearer token", req, snapshotPath)
	}
}

func TestSnapshotSurfacesServerError(t *testing.T) {
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
	}))

	if _, err := client.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("Snapshot error = %v, want server reason", err)
	}
}

func TestDecidePostsCommandToServer(t *testing.T) {
	client, seen := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"command":"approve","watchID":"w1","applied":true,"decisionID":"d1"}`)
	}))

	receipt, err := client.Decide(context.Background(), Decision{Command: "approve", WatchID: "w1"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !receipt.OK || !receipt.Applied || receipt.DecisionID != "d1" {
		t.Errorf("receipt = %+v, want ok applied d1", receipt)
	}
	req := nextRequest(t, seen)
	if req.method != http.MethodPost || req.path != decidePath {
		t.Errorf("request = %+v, want POST %s", req, decidePath)
	}
	if req.contentTyp != "application/json" || req.auth != "Bearer secret" {
		t.Errorf("headers = %s %s, want json and bearer token", req.contentTyp, req.auth)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatalf("decode posted decision: %v", err)
	}
	if body["command"] != "approve" || body["watchID"] != "w1" {
		t.Errorf("decision body = %v, want command=approve watchID=w1", body)
	}
}

func TestDecideRejectsRefusalAndEmptyInput(t *testing.T) {
	client, seen := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":false,"status":"DENIED"}`)
	}))

	if _, err := client.Decide(context.Background(), Decision{Command: "cancel", WatchID: "w1"}); err == nil {
		t.Fatal("Decide returned no error for a refused command")
	}
	nextRequest(t, seen)

	if _, err := client.Decide(context.Background(), Decision{WatchID: "w1"}); err == nil {
		t.Fatal("Decide returned no error for an empty command")
	}
	select {
	case req := <-seen:
		t.Errorf("empty command sent a request: %+v", req)
	default:
	}
}

func TestEventsParseFramesAndResumeWithLastEventID(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		n := requestCount
		mu.Unlock()
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("server writer is not a flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		if n == 1 {
			fmt.Fprint(w, ": heartbeat\n\n")
			fmt.Fprint(w, "id: 3\nevent: watch.updated\ndata: {\"watchID\":\"w1\"}\n\n")
			fmt.Fprint(w, "id: 4\nevent: blocker.opened\ndata: {\"id\":\"b1\"}\n\n")
		} else {
			fmt.Fprintf(w, "id: 5\nevent: message\ndata: %s\n\n", r.Header.Get("Last-Event-ID"))
		}
		flusher.Flush()
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, err := collectEvents(ctx, client.Events(ctx))
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].ID != "3" || events[0].Type != "watch.updated" || !strings.Contains(string(events[0].Data), "w1") {
		t.Errorf("first event = %+v", events[0])
	}
	if events[1].ID != "4" || events[1].Type != "blocker.opened" {
		t.Errorf("second event = %+v", events[1])
	}

	resumed, err := collectEvents(ctx, client.Events(ctx))
	if err != nil {
		t.Fatalf("resumed Events: %v", err)
	}
	if len(resumed) != 1 || !strings.Contains(string(resumed[0].Data), "4") {
		t.Errorf("resumed events = %+v, want data carrying Last-Event-ID 4", resumed)
	}
}

// TestColdStartEventTail checks that a cold live screen asks for a replay tail
// and receives a non-empty one. Before TUI-03 the first event request carried
// no replay parameter, so this test failed; the merged client sends it.
func TestColdStartEventTail(t *testing.T) {
	var mu sync.Mutex
	var replay, lastID string
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case snapshotPath:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"pointStats":{"used":7,"budget":500},"liveWatches":[{"id":"w1","pr":42,"subscribers":[{"id":"ses-1","role":"executor","target":"w1","phase":"watching"}]}]}`)
		case eventsPath:
			mu.Lock()
			replay = r.URL.Query().Get("replay")
			lastID = r.Header.Get("Last-Event-ID")
			mu.Unlock()
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("server writer is not a flusher")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			fmt.Fprint(w, "id: 11\nevent: watch.updated\ndata: {\"watchID\":\"w1\"}\n\n")
			fmt.Fprint(w, "id: 12\nevent: blocker.opened\ndata: {\"id\":\"b1\"}\n\n")
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	}))

	snap, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.PointStats.Used != 7 || snap.PointStats.Limit != 500 {
		t.Errorf("points = %+v, want 7/500", snap.PointStats)
	}
	if len(snap.LiveWatches) != 1 || len(snap.LiveWatches[0].Subscribers) != 1 {
		t.Fatalf("watches = %+v, want one watch with a subscriber", snap.LiveWatches)
	}
	sub := snap.LiveWatches[0].Subscribers[0]
	if sub.ID != "ses-1" || sub.Role != "executor" || sub.Target != "w1" {
		t.Errorf("subscriber = %+v, want role executor and target w1", sub)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, err := collectEvents(ctx, client.Events(ctx))
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("event tail = %+v, want both replay frames", events)
	}
	if events[0].ID != "11" || events[1].ID != "12" {
		t.Errorf("event tail = %+v, want ids 11 and 12", events)
	}
	if line := eventLine(events[0]); line == "" {
		t.Error("first replay frame produced an empty tail line")
	}

	mu.Lock()
	defer mu.Unlock()
	if replay != strconv.Itoa(eventReplayCount) {
		t.Errorf("cold replay = %q, want %d", replay, eventReplayCount)
	}
	if lastID != "" {
		t.Errorf("cold request sent Last-Event-ID %q, want none", lastID)
	}
}

func collectEvents(ctx context.Context, ch <-chan EventResult) ([]Event, error) {
	var events []Event
	for {
		select {
		case result, ok := <-ch:
			if !ok {
				return events, nil
			}
			if result.Err != nil {
				return events, result.Err
			}
			events = append(events, result.Event)
		case <-ctx.Done():
			return events, ctx.Err()
		}
	}
}

func TestNewClientRejectsRemoteEndpoint(t *testing.T) {
	if _, err := NewClient("http://example.com", "", nil); !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("remote endpoint error = %v, want ErrNotLoopback", err)
	}
	if _, err := NewClient("ftp://127.0.0.1", "", nil); err == nil {
		t.Fatal("non-http loopback endpoint was accepted")
	}
	if _, err := NewClient("http://localhost:8080", "", nil); err != nil {
		t.Fatalf("localhost endpoint rejected: %v", err)
	}
}

func TestViewCachesRenderUntilSnapshotChanges(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reads++
		n := reads
		mu.Unlock()
		fmt.Fprintf(w, `{"liveWatches":[{"id":"w%d","pr":%d,"revision":%d}]}`, n, n, n)
	}))

	app := NewApp(client)
	renders := 0
	app.render = func(s Snapshot) string {
		renders++
		if len(s.LiveWatches) == 0 {
			return "empty"
		}
		return fmt.Sprintf("pr=%d", s.LiveWatches[0].PR)
	}

	if err := app.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := app.View(); got != "pr=1" {
		t.Fatalf("first view = %q, want pr=1", got)
	}
	if got := app.View(); got != "pr=1" {
		t.Fatalf("cached view = %q, want pr=1", got)
	}
	if renders != 1 {
		t.Fatalf("render calls before refresh = %d, want 1", renders)
	}

	if err := app.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := app.View(); got != "pr=2" {
		t.Fatalf("view after refresh = %q, want pr=2", got)
	}
	if renders != 2 {
		t.Fatalf("render calls after refresh = %d, want 2", renders)
	}
}

func TestCommandSendsDecisionAndRefreshes(t *testing.T) {
	client, seen := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == snapshotPath {
			fmt.Fprint(w, `{"liveWatches":[{"id":"w1","pr":9,"revision":1}]}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"applied":true}`)
	}))

	app := NewApp(client)
	if err := app.Command(context.Background(), "ack w1"); err != nil {
		t.Fatalf("Command: %v", err)
	}
	var decisions, snapshots int
	for i := 0; i < 2; i++ {
		req := nextRequest(t, seen)
		switch req.path {
		case decidePath:
			decisions++
			if !strings.Contains(req.body, `"command":"approve"`) {
				t.Errorf("decision body = %s, want approve", req.body)
			}
		case snapshotPath:
			snapshots++
		}
	}
	if decisions != 1 || snapshots != 1 {
		t.Errorf("requests: decisions=%d snapshots=%d, want 1 each", decisions, snapshots)
	}
	if len(app.Snapshot().LiveWatches) != 1 {
		t.Errorf("snapshot after command = %+v, want one watch", app.Snapshot())
	}
}
