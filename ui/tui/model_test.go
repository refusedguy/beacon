package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{"ascii unchanged", "abcdef", 10, "abcdef"},
		{"ascii clipped", "abcdef", 4, "abc…"},
		{"multibyte fits by rune count", "ééé", 4, "ééé"},
		{"cjk clipped", "日本語テスト", 4, "日本語…"},
		{"emoji clipped", "a😀b😀c", 4, "a😀b…"},
		{"cjk then ascii clipped", "界ab", 2, "界…"},
		{"non-positive limit", "界", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.input, tc.limit)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) produced invalid UTF-8: %q", tc.input, tc.limit, got)
			}
		})
	}
}

func TestStreamHTTPClientIsFresh(t *testing.T) {
	transport := &http.Transport{}
	base := &http.Client{Timeout: requestTimeout, Transport: transport}
	c := &Client{HTTP: base}

	streamClient := c.streamHTTPClient()
	if streamClient == base {
		t.Fatal("stream client must be a fresh *http.Client, not the base")
	}
	if streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want 0", streamClient.Timeout)
	}
	if streamClient.Transport != transport {
		t.Fatal("stream client must reuse the configured transport")
	}
	if again := c.streamHTTPClient(); again == streamClient {
		t.Fatal("each call must return a fresh client")
	}
}

// TestEventRefreshesRenderedRuns drives the path main.go wires together. A
// real SSE event becomes a refreshMsg, the reload command fetches runs, and
// the resulting runsMsg renders the run in View.
func TestEventRefreshesRenderedRuns(t *testing.T) {
	const runID = "run-42"
	const taskID = "T-functional"
	runJSON := `{"runs":[{"id":"` + runID + `","task_id":"` + taskID + `","state":"ready","pr":42,"head_sha":"abc123","reason":"review approved","updated_at":"2026-11-25T12:00:00Z"}]}`
	eventJSON := `{"id":9,"type":"run.updated","run_id":"` + runID + `","at":"2026-11-25T12:00:00Z"}`

	runsHits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiEventsPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+eventJSON+"\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case apiRunsPath:
			select {
			case runsHits <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, runJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok")
	m := newModel(client)

	events := make(chan Event, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- client.Stream(ctx, func(ev Event) {
			select {
			case events <- ev:
			default:
			}
		})
	}()

	var ev Event
	select {
	case ev = <-events:
	case <-ctx.Done():
		t.Fatal("no event received from /api/v1/events")
	}
	if ev.ID != 9 || ev.Type != "run.updated" || ev.RunID != runID {
		t.Fatalf("event: %+v", ev)
	}

	// main.go discards the event body and sends refreshMsg to the program.
	next, cmd := m.Update(refreshMsg{})
	if cmd == nil {
		t.Fatal("Update(refreshMsg) returned no reload command")
	}
	raw := cmd()
	msg, ok := raw.(runsMsg)
	if !ok {
		t.Fatalf("reload command returned %T, want runsMsg", raw)
	}
	if msg.err != nil {
		t.Fatalf("reload: %v", msg.err)
	}
	select {
	case <-runsHits:
	default:
		t.Fatal("reload command did not request /api/v1/runs")
	}

	finalModel, followUp := next.Update(msg)
	if followUp != nil {
		t.Fatalf("runsMsg returned an unexpected command: %v", followUp)
	}
	final, ok := finalModel.(model)
	if !ok {
		t.Fatalf("Update(runsMsg) returned %T, want model", finalModel)
	}
	view := final.View()
	if !strings.Contains(view, runID) {
		t.Fatalf("View dropped run %q:\n%s", runID, view)
	}
	if !strings.Contains(view, taskID) {
		t.Fatalf("View dropped task %q:\n%s", taskID, view)
	}
	if !strings.Contains(view, "loaded 1 run(s)") {
		t.Fatalf("View status missing loaded count:\n%s", view)
	}

	cancel()
	if err := <-streamDone; err != nil {
		t.Fatalf("Stream: %v", err)
	}
	t.Logf("event id=%d type=%s run_id=%s", ev.ID, ev.Type, ev.RunID)
	t.Logf("reload command returned runsMsg with %d run(s)", len(msg.runs))
	t.Logf("rendered line: %q", strings.Split(view, "\n")[1])
}
