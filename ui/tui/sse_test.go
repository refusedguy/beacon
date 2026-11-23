package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadSSE(t *testing.T) {
	stream := ": heartbeat\n\nid: 4\nevent: run.updated\ndata: {\"id\":4,\"type\":\"run.updated\",\"run_id\":\"run-1\"}\n\nid: 5\ndata: {\"type\":\"run.done\",\"run_id\":\"run-2\"}\n\n"
	var frames []sseFrame
	if err := readSSE(strings.NewReader(stream), func(f sseFrame) { frames = append(frames, f) }); err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames: %d: %+v", len(frames), frames)
	}
	if frames[0].id != "4" || frames[0].event != "run.updated" || !strings.Contains(frames[0].data, "run-1") {
		t.Fatalf("first frame: %+v", frames[0])
	}
	if frames[1].id != "5" || !strings.Contains(frames[1].data, "run-2") {
		t.Fatalf("second frame: %+v", frames[1])
	}
}

func TestStreamEvents(t *testing.T) {
	var gotPath, gotAccept, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept, gotAuth = r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":7,\"type\":\"run.updated\",\"run_id\":\"run-7\"}\n\n"))
	}))
	defer srv.Close()

	events := make(chan Event, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- NewClient(srv.URL, "tok").Stream(ctx, func(ev Event) {
			select {
			case events <- ev:
			default:
			}
		})
	}()

	select {
	case ev := <-events:
		if ev.ID != 7 || ev.Type != "run.updated" || ev.RunID != "run-7" {
			t.Fatalf("event: %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no event received")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotPath != apiEventsPath || !strings.Contains(gotAccept, "text/event-stream") || gotAuth != "Bearer tok" {
		t.Fatalf("request: path=%s accept=%q auth=%q", gotPath, gotAccept, gotAuth)
	}
}
