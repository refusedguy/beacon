package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// App holds the last snapshot and the view rendered from it. The View method
// caches its work and rebuilds only after a refresh changes the snapshot.
// There is no widget framework: a dashboard is a string a caller can print to
// any terminal.
type App struct {
	client *Client
	render func(Snapshot) string

	mu            sync.Mutex
	snap          Snapshot
	revision      uint64
	lastErr       error
	cacheRevision uint64
	cachedView    string
}

// NewApp builds an app around a client. The default renderer sorts watches by
// revision and formats them into a plain-text table.
func NewApp(client *Client) *App {
	return &App{client: client, render: Render}
}

// Refresh reads one snapshot from the server and advances the revision. A
// failed read keeps the previous snapshot and records the error.
func (a *App) Refresh(ctx context.Context) error {
	snap, err := a.client.Snapshot(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.lastErr = err
		return err
	}
	a.snap = snap
	a.lastErr = nil
	a.revision++
	return nil
}

// Snapshot returns the last snapshot the app read.
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snap
}

// LastError returns the error from the most recent refresh, if any.
func (a *App) LastError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

// View returns the rendered dashboard. The expensive render runs once per
// revision, so callers can draw often without rebuilding the same table.
func (a *App) View() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cacheRevision == a.revision {
		return a.cachedView
	}
	a.cachedView = a.render(a.snap)
	a.cacheRevision = a.revision
	return a.cachedView
}

// FirstFrame renders the current snapshot once without touching the cached
// view. A live-server probe calls Refresh then FirstFrame and times the pair,
// which records the time to first frame against a real listener rather than a
// fake socket.
func (a *App) FirstFrame() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.render(a.snap)
}

// Decide sends one command through the REST route, then refreshes so the view
// shows the server's answer rather than a local guess.
func (a *App) Decide(ctx context.Context, d Decision) error {
	if _, err := a.client.Decide(ctx, d); err != nil {
		return err
	}
	return a.Refresh(ctx)
}

// Command parses one line of dashboard input. "ack" is the familiar name for
// the server's "approve" verb; the other names map to the verbs themselves.
func (a *App) Command(ctx context.Context, line string) error {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "r", "refresh":
		return a.Refresh(ctx)
	case "ack", "approve", "reject", "rework", "pause", "resume", "unblock", "cancel":
		if len(fields) < 2 {
			return errors.New("tui: command needs a watch id")
		}
		verb := fields[0]
		if verb == "ack" {
			verb = "approve"
		}
		return a.Decide(ctx, Decision{Command: verb, WatchID: fields[1]})
	default:
		return fmt.Errorf("tui: unknown command %q", fields[0])
	}
}

// Run refreshes once, draws the dashboard, then redraws on every SSE event.
// A closed stream is reopened after a short pause with Last-Event-ID, so a
// dropped connection does not replay events the app already saw.
func (a *App) Run(ctx context.Context, out io.Writer) error {
	if err := a.Refresh(ctx); err != nil {
		return err
	}
	if _, err := io.WriteString(out, a.View()); err != nil {
		return err
	}
	for {
		stream := a.client.Events(ctx)
		for result := range stream {
			if result.Err != nil {
				break
			}
			if err := a.Refresh(ctx); err != nil {
				return err
			}
			if _, err := io.WriteString(out, a.View()); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectDelay):
		}
	}
}

// Render builds the dashboard text. Watches are sorted by descending revision
// so the newest state is always at the top. It reads only its argument and
// has no side effects, which keeps the cached View stable.
func Render(s Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "beacon | points %d/%d | watches %d\n", s.PointStats.Used, s.PointStats.Limit, len(s.LiveWatches))
	watches := append([]Watch(nil), s.LiveWatches...)
	sort.SliceStable(watches, func(i, j int) bool {
		return watches[i].Revision > watches[j].Revision
	})
	for _, w := range watches {
		blocked := ""
		if w.Blocked != nil && w.Blocked.Reason.Valid() {
			blocked = " blocked=" + string(w.Blocked.Reason)
		}
		fmt.Fprintf(&b, "#%d %s %s verdict=%s rev=%d%s\n", w.PR, shortSHA(w.HeadSHA), w.Status, w.Verdict, w.Revision, blocked)
	}
	for _, incident := range s.BlockerIncidents {
		fmt.Fprintf(&b, "blocker %s %s: %s\n", incident.ID, incident.Kind, incident.Reason)
	}
	for _, escalation := range s.Escalations {
		fmt.Fprintf(&b, "escalation %s\n", escalation)
	}
	for _, loadErr := range s.LoadErrors {
		fmt.Fprintf(&b, "load error %s\n", loadErr)
	}
	return b.String()
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
