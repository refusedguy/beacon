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

// TestStreamedEventTriggersRunsRead proves the live wiring passes a decoded
// SSE event into refreshMsg and that an event for a known run schedules a runs
// read. The daemon's runs response, not the event body, is the run state.
func TestStreamedEventTriggersRunsRead(t *testing.T) {
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
			_, _ = io.WriteString(w, `{"runs":[{"id":"`+runID+`","state":"idle"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok")
	m := newModel(client)

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

	_, cmd := m.Update(refresh)
	if cmd == nil {
		t.Fatal("a known-run event must schedule a runs read")
	}
	raw := cmd()
	msg, ok := raw.(runsMsg)
	if !ok || msg.err != nil {
		t.Fatalf("reload command = %T %v, want runsMsg with no error", raw, msg.err)
	}
	if got := atomic.LoadInt64(&runsGets); got != 1 {
		t.Fatalf("/api/v1/runs GETs = %d, want 1", got)
	}
	if len(msg.runs) != 1 || msg.runs[0].ID != runID {
		t.Fatalf("reload returned %+v, want run %q", msg.runs, runID)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("forwardEvents: %v", err)
	}
}
