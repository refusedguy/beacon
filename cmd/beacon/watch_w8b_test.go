package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/httpapi"
)

func w8bSnapshot() httpapi.ClientSnapshot {
	return httpapi.ClientSnapshot{
		Project:      "beacon",
		Points:       httpapi.ClientPoints{Used: 42, Limit: 500},
		Cycle:        httpapi.ClientCycle{ID: "cycle-9", Duration: "1.5s"},
		Orchestrator: httpapi.ClientOrchestrator{Live: true, Session: "ses-orch"},
		Watches: []httpapi.ClientWatch{
			{PR: "#407", Watch: "w-1", Verdict: "ready", Status: "watching", Blocker: "b-2", Subscriber: "ses-1", Phase: "executor", FixAttempts: 1, WakeAttempts: 3, Updated: "12:00"},
			{Watch: "w-2", Verdict: "needs_input", Status: "paused"},
		},
		Attention:   []string{"approval needed"},
		Escalations: []string{"base red", "gate retry"},
	}
}

func TestW8BRenderHTTPSnapshotHasHeaderTableAndBlocks(t *testing.T) {
	var out bytes.Buffer
	if err := renderHTTPSnapshot(&out, w8bSnapshot(), "table"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"PROJECT: beacon", "POINTS: 42/500", "CYCLE: cycle-9 (1.5s)", "ORCHESTRATOR: live ses-orch",
		"PR", "WATCH", "VERDICT", "STATUS(BLOCKER)", "SUBSCRIBER(PHASE)", "FIX+WAKE ATTEMPTS", "UPDATED",
		"watching(b-2)", "ses-1(executor)", "1+3", "ATTENTION: approval needed",
		"ESCALATIONS: base red; gate retry",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("нет %q в выводе:\n%s", want, text)
		}
	}
}

func TestW8BRenderHTTPSnapshotDashesEmptyCells(t *testing.T) {
	var out bytes.Buffer
	if err := renderHTTPSnapshot(&out, httpapi.ClientSnapshot{Watches: []httpapi.ClientWatch{{Watch: "w-2"}}}, "table"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "POINTS: 0/500") || !strings.Contains(text, "ORCHESTRATOR: offline") {
		t.Fatalf("шапка = %q", text)
	}
	if !strings.Contains(text, "ATTENTION: -") || !strings.Contains(text, "ESCALATIONS: -") {
		t.Fatalf("пустые блоки не прочеркнуты: %q", text)
	}
}

func TestW8BRenderHTTPSnapshotJSON(t *testing.T) {
	var out bytes.Buffer
	if err := renderHTTPSnapshot(&out, w8bSnapshot(), "json"); err != nil {
		t.Fatal(err)
	}
	var decoded httpapi.ClientSnapshot
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("json: %v: %q", err, out.String())
	}
	if decoded.Project != "beacon" || len(decoded.Watches) != 2 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestW8BLiveFailsClearlyWithoutConnection(t *testing.T) {
	dir := t.TempDir()
	prevDir, prevRunner := watchHTTPStateDir, watchLiveRunner
	watchHTTPStateDir = func() (string, error) { return dir, nil }
	watchLiveRunner = runWatchLiveHTTP
	t.Cleanup(func() { watchHTTPStateDir, watchLiveRunner = prevDir, prevRunner })
	c, out, errOut, _ := w69CLI(t, nil)

	if code := runWatch(c, []string{"--live"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "connection.json") {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

// w8bPort is a stand-in for the HTTP source. Every Snapshot call notifies the
// test through rendered so it can wait for a refresh deterministically.
type w8bPort struct {
	snap     httpapi.ClientSnapshot
	err      error
	calls    int
	rendered chan struct{}
	events   []chan httpapi.ClientEventResult
	seen     int
}

func (p *w8bPort) Snapshot(context.Context) (httpapi.ClientSnapshot, error) {
	p.calls++
	select {
	case p.rendered <- struct{}{}:
	default:
	}
	return p.snap, p.err
}

func (p *w8bPort) Events(context.Context) <-chan httpapi.ClientEventResult {
	i := p.seen
	p.seen++
	if i < len(p.events) {
		return p.events[i]
	}
	return make(chan httpapi.ClientEventResult)
}

func TestW8BLiveReadsHTTPProjectionOnEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connection.json"), []byte(`{"host":"127.0.0.1","port":8787,"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first := make(chan httpapi.ClientEventResult, 1)
	first <- httpapi.ClientEventResult{Event: httpapi.ClientEvent{ID: "1", Type: "watch"}}
	close(first)
	port := &w8bPort{snap: w8bSnapshot(), rendered: make(chan struct{}, 16), events: []chan httpapi.ClientEventResult{first}}

	prevDir, prevClient, prevRunner, prevCtx, prevDelay := watchHTTPStateDir, watchHTTPClient, watchLiveRunner, watchLiveContext, watchReconnectDelay
	watchHTTPStateDir = func() (string, error) { return dir, nil }
	watchHTTPClient = func(httpapi.ClientConn) (watchHTTPPort, error) { return port, nil }
	watchLiveRunner = runWatchLiveHTTP
	watchReconnectDelay = 0
	ctx, cancel := context.WithCancel(context.Background())
	watchLiveContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() {
		watchHTTPStateDir, watchHTTPClient, watchLiveRunner = prevDir, prevClient, prevRunner
		watchLiveContext, watchReconnectDelay = prevCtx, prevDelay
	})

	c, out, errOut, _ := w69CLI(t, nil)
	done := make(chan int, 1)
	go func() { done <- runWatch(c, []string{"--live", "--interval", "0"}) }()
	for i := 0; i < 3; i++ {
		select {
		case <-port.rendered:
		case <-time.After(2 * time.Second):
			t.Fatalf("не дождались рендера %d: stderr %q", i, errOut.String())
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code = %d, stderr %q", code, errOut.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("живой цикл не остановился")
	}
	if port.calls < 3 {
		t.Fatalf("снимков %d, ожидалось >= 3", port.calls)
	}
	for _, want := range []string{"PROJECT: beacon", "STATUS(BLOCKER)", "watching(b-2)", "ESCALATIONS: base red; gate retry"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("нет %q в выводе:\n%s", want, out.String())
		}
	}
}
