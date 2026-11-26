package main

import (
	"context"
	"fmt"
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
	if frames[0].event != "run.updated" || !strings.Contains(frames[0].data, "run-1") {
		t.Fatalf("first frame: %+v", frames[0])
	}
	if !strings.Contains(frames[1].data, "run-2") {
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

// TestReadSSEDropsPartialFrameAtEOF proves that a frame without a terminating
// blank line is not delivered, and that the parser keeps the frame id so the
// next connection can ask the daemon to resume.
func TestReadSSEDropsPartialFrameAtEOF(t *testing.T) {
	stream := "id: 4\ndata: {\"type\":\"complete\"}\n\nid: 5\ndata: {\"type\":\"partial\"}"
	var frames []sseFrame
	if err := readSSE(strings.NewReader(stream), func(f sseFrame) { frames = append(frames, f) }); err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("frames: %d: %+v", len(frames), frames)
	}
	if frames[0].id != "4" || !strings.Contains(frames[0].data, "complete") {
		t.Fatalf("frame: %+v", frames[0])
	}
}

// TestStreamReconnectResumesAndForcesRead proves that a reconnect sends the
// last frame id back to the daemon and emits an internal notification that
// makes the monitor read runs again. The server closes after each frame, so a
// notification dropped while the socket was down is recovered by that read.
func TestStreamReconnectResumesAndForcesRead(t *testing.T) {
	ids := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 7\ndata: {\"id\":7,\"type\":\"run.updated\",\"run_id\":\"run-recover\"}\n\n")
	}))
	defer srv.Close()

	events := make(chan Event, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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

	// The first connection has no resume id and delivers one real event.
	select {
	case id := <-ids:
		if id != "" {
			t.Fatalf("first Last-Event-ID = %q, want empty", id)
		}
	case <-ctx.Done():
		t.Fatal("stream did not connect")
	}
	select {
	case ev := <-events:
		if ev.ID != 7 || ev.RunID != "run-recover" {
			t.Fatalf("event: %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no event on the first connection")
	}

	// The reconnect sends the last id and forces a runs read notification
	// before it delivers any new event.
	select {
	case id := <-ids:
		if id != "7" {
			t.Fatalf("reconnect Last-Event-ID = %q, want 7", id)
		}
	case <-ctx.Done():
		t.Fatal("stream did not reconnect")
	}
	select {
	case ev := <-events:
		if ev.Type != reconnectEventType {
			t.Fatalf("reconnect notification = %+v, want type %q", ev, reconnectEventType)
		}
	case <-ctx.Done():
		t.Fatal("reconnect did not force a read")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Stream: %v", err)
	}
}
