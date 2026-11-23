package tui

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeLiveProgram stands in for tea.Program so a test can end a run without a
// terminal. Quit and pressQ both end the run through the same Once.
type fakeLiveProgram struct {
	msgs     chan tea.Msg
	quit     chan struct{}
	returnCh chan struct{}

	quitOnce   sync.Once
	returnOnce sync.Once
}

func newFakeLiveProgram() *fakeLiveProgram {
	return &fakeLiveProgram{
		msgs:     make(chan tea.Msg, 64),
		quit:     make(chan struct{}),
		returnCh: make(chan struct{}),
	}
}

func (p *fakeLiveProgram) Send(msg tea.Msg) {
	select {
	case p.msgs <- msg:
	default:
	}
}

func (p *fakeLiveProgram) Quit() {
	p.quitOnce.Do(func() {
		close(p.quit)
		p.end()
	})
}

func (p *fakeLiveProgram) Run() (tea.Model, error) {
	<-p.returnCh
	return nil, nil
}

// pressQ simulates the operator leaving the screen.
func (p *fakeLiveProgram) pressQ() { p.end() }

func (p *fakeLiveProgram) end() {
	p.returnOnce.Do(func() { close(p.returnCh) })
}

func setLiveProgram(p liveProgram) func() {
	old := newLiveProgram
	newLiveProgram = func(*App, context.Context, []Card) liveProgram { return p }
	return func() { newLiveProgram = old }
}

type runLiveDoerFunc func(*http.Request) (*http.Response, error)

func (f runLiveDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

type runLiveCounters struct {
	mu        sync.Mutex
	snapshots int
	events    int
}

func (c *runLiveCounters) record(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch path {
	case snapshotPath:
		c.snapshots++
	case eventsPath:
		c.events++
	}
}

func (c *runLiveCounters) values() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshots, c.events
}

// runLiveTestClient serves one SSE frame then holds the stream open until the
// request context is cancelled, like the real event endpoint does.
func runLiveTestClient(t *testing.T, c *runLiveCounters) *Client {
	t.Helper()
	doer := runLiveDoerFunc(func(req *http.Request) (*http.Response, error) {
		c.record(req.URL.Path)
		if req.URL.Path == eventsPath {
			pr, pw := io.Pipe()
			go func() {
				_, _ = io.WriteString(pw, "id: 1\nevent: message\ndata: tick\n\n")
				<-req.Context().Done()
				_ = pw.Close()
			}()
			return &http.Response{StatusCode: http.StatusOK, Body: pr, Header: make(http.Header)}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})
	client, err := NewClient("http://127.0.0.1:1", "", doer)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func waitForRunLive(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunLiveStopsPollingOnQuit(t *testing.T) {
	base := runtime.NumGoroutine()
	c := &runLiveCounters{}
	app := NewApp(runLiveTestClient(t, c))

	program := newFakeLiveProgram()
	defer setLiveProgram(program)()

	done := make(chan error, 1)
	go func() { done <- app.RunLive(context.Background(), nil) }()

	waitForRunLive(t, "first snapshot refresh", func() bool {
		s, e := c.values()
		return s > 1 && e > 0
	})

	program.pressQ()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLive: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunLive did not return after quit")
	}

	s1, e1 := c.values()
	time.Sleep(60 * time.Millisecond)
	s2, e2 := c.values()
	if s1 != s2 || e1 != e2 {
		t.Fatalf("polling continued after exit: snapshots %d -> %d, events %d -> %d", s1, s2, e1, e2)
	}

	waitForRunLive(t, "goroutines to settle", func() bool {
		return runtime.NumGoroutine() <= base+1
	})
}

func TestRunLiveStopsOnContextCancel(t *testing.T) {
	c := &runLiveCounters{}
	app := NewApp(runLiveTestClient(t, c))

	program := newFakeLiveProgram()
	defer setLiveProgram(program)()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunLive(ctx, nil) }()

	waitForRunLive(t, "first SSE subscription", func() bool {
		_, e := c.values()
		return e > 0
	})

	cancel()
	program.pressQ()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLive: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunLive did not return after cancel")
	}

	s1, e1 := c.values()
	time.Sleep(60 * time.Millisecond)
	s2, e2 := c.values()
	if s1 != s2 || e1 != e2 {
		t.Fatalf("polling continued after cancel: snapshots %d -> %d, events %d -> %d", s1, s2, e1, e2)
	}
}
