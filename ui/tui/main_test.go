package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// captureSender records messages sent by the stream forwarder.
type captureSender struct {
	msgs chan tea.Msg
}

func (s *captureSender) Send(msg tea.Msg) {
	select {
	case s.msgs <- msg:
	default:
	}
}

// TestStreamedEventSkipsRunsRead proves the live wiring passes a decoded SSE
// event into refreshMsg. One event for a known run reaches refreshMsg.event,
// and the model applies it as a delta without requesting /api/v1/runs.
func TestStreamedEventSkipsRunsRead(t *testing.T) {
	const runID = "run-live"
	const at = "2026-11-26T11:00:00Z"
	eventJSON := `{"id":9,"type":"idle","run_id":"` + runID + `","at":"` + at + `"}`

	var runsGets int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiEventsPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+eventJSON+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		case apiRunsPath:
			atomic.AddInt64(&runsGets, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"runs":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok")
	// The run is already indexed, so the delta path needs no initial runs read.
	m := model{
		client: client,
		runs:   []Run{{ID: runID, State: "waiting_ci"}},
		byID:   map[string]int{runID: 0},
		status: "ready",
	}

	sender := &captureSender{msgs: make(chan tea.Msg, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- forwardEvents(ctx, client, sender) }()

	var refresh refreshMsg
	select {
	case raw := <-sender.msgs:
		var ok bool
		refresh, ok = raw.(refreshMsg)
		if !ok {
			t.Fatalf("forwarded %T, want refreshMsg", raw)
		}
	case <-ctx.Done():
		t.Fatal("no refreshMsg forwarded from the stream")
	}

	want := Event{ID: 9, Type: "idle", RunID: runID, At: at}
	if refresh.event != want {
		t.Fatalf("refreshMsg.event = %+v, want %+v", refresh.event, want)
	}

	next, cmd := m.Update(refresh)
	if cmd != nil {
		t.Fatal("a known-run event produced a reload command")
	}
	if got := atomic.LoadInt64(&runsGets); got != 0 {
		t.Fatalf("/api/v1/runs GETs = %d, want 0", got)
	}
	final, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	if i := final.byID[runID]; final.runs[i].UpdatedAt != at {
		t.Fatalf("event not applied to indexed run: %+v", final.runs[i])
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("forwardEvents: %v", err)
	}
}
