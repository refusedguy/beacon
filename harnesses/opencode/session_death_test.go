package opencode

import (
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

func TestWatchIdleEmitsSessionDead(t *testing.T) {
	const session = "ses_dead"

	frames := []struct {
		name  string
		event string
		data  string
	}{
		{
			name:  "user cancel",
			event: "session.execution.interrupted",
			data:  `{"type":"session.execution.interrupted","properties":{"sessionID":"ses_dead","reason":"user"}}`,
		},
		{
			name:  "server shutdown",
			event: "session.execution.interrupted",
			data:  `{"type":"session.execution.interrupted","properties":{"sessionID":"ses_dead","reason":"shutdown"}}`,
		},
		{
			name:  "delete",
			event: "session.deleted",
			data:  `{"type":"session.deleted","properties":{"sessionID":"ses_dead"}}`,
		},
		{
			name:  "cancel alias",
			event: "session.canceled",
			data:  `{"type":"session.canceled","properties":{"sessionID":"ses_dead"}}`,
		},
	}

	for _, tc := range frames {
		t.Run(tc.name, func(t *testing.T) {
			connected := make(chan struct{})
			send := make(chan struct{})
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/event" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				once.Do(func() { close(connected) })
				<-send
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", tc.event, tc.data)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()

			h := New(Config{Endpoint: srv.URL})
			h.bind(session, "run-7")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.WatchIdle(ctx)

			select {
			case <-connected:
			case <-time.After(3 * time.Second):
				t.Fatal("SSE subscription not established")
			}
			close(send)

			select {
			case ev := <-h.Events():
				if ev.Kind != eventKindSessionDead {
					t.Errorf("kind = %q, want %q", ev.Kind, eventKindSessionDead)
				}
				if ev.RunID != core.RunID("run-7") {
					t.Errorf("runID = %q, want run-7", ev.RunID)
				}
				if ev.SessionID != session {
					t.Errorf("sessionID = %q, want %q", ev.SessionID, session)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no session death event received")
			}
		})
	}
}

func TestSupersededInterruptDoesNotEndSession(t *testing.T) {
	const session = "ses_busy"
	connected := make(chan struct{})
	send := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/event" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		once.Do(func() { close(connected) })
		<-send
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: session.execution.interrupted\ndata: {\"properties\":{\"sessionID\":\"ses_busy\",\"reason\":\"superseded\"}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	h := New(Config{Endpoint: srv.URL})
	h.bind(session, "run-7")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.WatchIdle(ctx)

	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE subscription not established")
	}
	close(send)

	select {
	case ev := <-h.Events():
		t.Fatalf("superseded turn emitted %q, want no death event", ev.Kind)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestIsAliveDistinguishesEndedFromBusy(t *testing.T) {
	cases := []struct {
		name      string
		session   string
		status    int
		body      string
		wantAlive bool
	}{
		{
			name:    "cancelled session",
			session: "ses_cancelled",
			body:    `{"id":"ses_cancelled","outcome":"interrupted","time":{"busy":100,"idle":200}}`,
		},
		{
			name:      "busy session",
			session:   "ses_busy",
			body:      `{"id":"ses_busy","time":{"busy":300}}`,
			wantAlive: true,
		},
		{
			name:      "idle live session",
			session:   "ses_idle",
			body:      `{"id":"ses_idle","time":{"idle":300}}`,
			wantAlive: true,
		},
		{
			name:    "deleted session",
			session: "ses_gone",
			status:  http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/session/"+tc.session {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = io.WriteString(w, tc.body)
				}
			}))
			defer srv.Close()

			h := New(Config{Endpoint: srv.URL})
			h.bind(tc.session, "run-7")
			alive, err := h.IsAlive(context.Background(), tc.session)
			if err != nil {
				t.Fatalf("IsAlive: %v", err)
			}
			if alive != tc.wantAlive {
				t.Fatalf("IsAlive = %v, want %v", alive, tc.wantAlive)
			}
		})
	}
}

func TestWakeConflictIsFailedDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/prompt") {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"conflict"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	h := New(Config{Endpoint: srv.URL})
	err := h.Wake(context.Background(), "ses_9", core.WakeContext{RunID: "run-7", Reason: "checks failed"})
	if err == nil {
		t.Fatal("Wake accepted a 409 response as delivery")
	}
}
