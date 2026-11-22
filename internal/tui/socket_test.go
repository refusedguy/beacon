package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// socketPath uses a short directory name because Unix socket paths are capped
// near 108 bytes on Linux.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bs39")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "c.sock")
}

func mustListen(t *testing.T, path string, handler Handler) *Server {
	t.Helper()
	srv, err := Listen(path, handler)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func mustDial(t *testing.T, path string) *ControlClient {
	t.Helper()
	client, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func sendRequest(t *testing.T, client *ControlClient, req SocketRequest) SocketResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := client.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send(%s): %v", req.Command, err)
	}
	return resp
}

// waitForValue receives one value from ch before ctx is done. The caller owns
// the deadline, so a stalled test fails on its own timeout instead of a fixed delay.
func waitForValue[T any](t *testing.T, ctx context.Context, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatalf("%s channel closed before a value arrived", what)
		}
		return v
	case <-ctx.Done():
		t.Fatalf("no %s before deadline: %v", what, ctx.Err())
		var zero T
		return zero
	}
}

func nextEvent(t *testing.T, ctx context.Context, client *ControlClient) SocketEvent {
	t.Helper()
	return waitForValue(t, ctx, client.Events(), "event")
}

func TestSocketConnectIsLocalAndRestricted(t *testing.T) {
	path := socketPath(t)
	srv := mustListen(t, path, HandlerFunc(func(context.Context, SocketRequest) (SocketResponse, error) {
		return SocketResponse{OK: true}, nil
	}))

	info, err := os.Stat(srv.Addr())
	if err != nil {
		t.Fatalf("Stat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("socket mode = %o, want 0600", got)
	}

	client := mustDial(t, srv.Addr())
	if client == nil {
		t.Fatal("Dial returned no client")
	}

	resp := sendRequest(t, client, SocketRequest{ID: "c1", Command: CmdAck, WatchID: "w1"})
	if !resp.OK {
		t.Fatalf("response refused: %s", resp.Error)
	}
}

func TestSocketCommandReachesHandler(t *testing.T) {
	path := socketPath(t)
	var mu sync.Mutex
	var seen []SocketRequest
	srv := mustListen(t, path, HandlerFunc(func(_ context.Context, req SocketRequest) (SocketResponse, error) {
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		return SocketResponse{OK: true, Applied: true, Status: "applied"}, nil
	}))
	client := mustDial(t, srv.Addr())

	req := SocketRequest{
		ID:         "cmd-1",
		Command:    CmdClaim,
		WatchID:    "w42",
		Revision:   9,
		Obligation: "obl-1",
		Note:       "taking it",
		Actor:      "ses_self",
	}
	resp := sendRequest(t, client, req)
	if !resp.OK || !resp.Applied {
		t.Fatalf("response = %+v, want OK and Applied", resp)
	}
	if resp.ID != req.ID || resp.WatchID != req.WatchID || resp.Command != req.Command {
		t.Errorf("response did not echo request: %+v", resp)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("handler called %d times, want 1", len(seen))
	}
	if seen[0] != req {
		t.Errorf("handler saw %+v, want %+v", seen[0], req)
	}
}

func TestSocketCommandIsIdempotentByWatchIDAndRevision(t *testing.T) {
	path := socketPath(t)
	var mu sync.Mutex
	calls := 0
	srv := mustListen(t, path, HandlerFunc(func(context.Context, SocketRequest) (SocketResponse, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return SocketResponse{OK: true, Applied: true}, nil
	}))
	client := mustDial(t, srv.Addr())

	req := SocketRequest{ID: "dup", Command: CmdAck, WatchID: "w7", Revision: 3}
	first := sendRequest(t, client, req)
	second := sendRequest(t, client, req)

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("handler called %d times for one watchID+revision, want 1", gotCalls)
	}
	if first != second {
		t.Errorf("duplicate answers differ: first=%+v second=%+v", first, second)
	}

	// A new revision is a distinct command and must reach the handler again.
	req.Revision = 4
	_ = sendRequest(t, client, req)
	mu.Lock()
	gotCalls = calls
	mu.Unlock()
	if gotCalls != 2 {
		t.Errorf("handler called %d times after a new revision, want 2", gotCalls)
	}
}

func TestSocketEventReachesAttachedAgent(t *testing.T) {
	path := socketPath(t)
	srv := mustListen(t, path, HandlerFunc(func(context.Context, SocketRequest) (SocketResponse, error) {
		return SocketResponse{OK: true}, nil
	}))
	client := mustDial(t, srv.Addr())
	// A completed round trip proves the accept loop registered this client.
	_ = sendRequest(t, client, SocketRequest{ID: "sync", Command: CmdAck, WatchID: "w1"})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)

	want := SocketEvent{Type: "refresh", WatchID: "w1", Data: json.RawMessage(`{"n":1}`)}
	srv.Publish(want)
	got := nextEvent(t, ctx, client)

	if got.Type != want.Type || got.WatchID != want.WatchID {
		t.Fatalf("event metadata = %+v, want %+v", got, want)
	}
	if string(got.Data) != string(want.Data) {
		t.Errorf("event data = %s, want %s", got.Data, want.Data)
	}
}

func TestSocketEventReachesEveryAgent(t *testing.T) {
	path := socketPath(t)
	srv := mustListen(t, path, HandlerFunc(func(context.Context, SocketRequest) (SocketResponse, error) {
		return SocketResponse{OK: true}, nil
	}))
	a := mustDial(t, srv.Addr())
	b := mustDial(t, srv.Addr())
	_ = sendRequest(t, a, SocketRequest{ID: "a", Command: CmdAck, WatchID: "w1"})
	_ = sendRequest(t, b, SocketRequest{ID: "b", Command: CmdAck, WatchID: "w1"})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)

	srv.Publish(SocketEvent{Type: "refresh"})
	if got := nextEvent(t, ctx, a).Type; got != "refresh" {
		t.Errorf("agent a event type = %q", got)
	}
	if got := nextEvent(t, ctx, b).Type; got != "refresh" {
		t.Errorf("agent b event type = %q", got)
	}
}

func TestSocketCloseDetachesAgentsAndUnlinks(t *testing.T) {
	path := socketPath(t)
	srv := mustListen(t, path, HandlerFunc(func(context.Context, SocketRequest) (SocketResponse, error) {
		return SocketResponse{OK: true}, nil
	}))
	client := mustDial(t, srv.Addr())
	_ = sendRequest(t, client, SocketRequest{ID: "before", Command: CmdAck, WatchID: "w1"})

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.Send(ctx, SocketRequest{ID: "after", Command: CmdAck, WatchID: "w1"}); err == nil {
		t.Fatal("Send succeeded after the server closed")
	}

	if _, err := Dial(context.Background(), path); err == nil {
		t.Fatal("Dial succeeded after the socket was closed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket file still present after Close: %v", err)
	}
}

func TestSocketErrors(t *testing.T) {
	path := socketPath(t)
	handler := HandlerFunc(func(_ context.Context, req SocketRequest) (SocketResponse, error) {
		if req.Note == "boom" {
			return SocketResponse{}, errors.New("handler refused")
		}
		return SocketResponse{OK: true, Applied: true}, nil
	})
	srv := mustListen(t, path, handler)
	client := mustDial(t, srv.Addr())

	resp := sendRequest(t, client, SocketRequest{ID: "no-watch", Command: CmdAck})
	if resp.OK || !strings.Contains(resp.Error, "watchID") {
		t.Errorf("empty watchID response = %+v, want refusal", resp)
	}

	resp = sendRequest(t, client, SocketRequest{ID: "bad-cmd", Command: Command("destroy"), WatchID: "w1"})
	if resp.OK || !strings.Contains(resp.Error, "unsupported command") {
		t.Errorf("bad command response = %+v, want refusal", resp)
	}

	resp = sendRequest(t, client, SocketRequest{ID: "boom", Command: CmdRelease, WatchID: "w1", Note: "boom"})
	if resp.OK || !strings.Contains(resp.Error, "handler refused") {
		t.Errorf("handler error response = %+v, want refusal", resp)
	}

	if _, err := Listen("", handler); err == nil {
		t.Error("Listen accepted an empty path")
	}
	if _, err := Listen(filepath.Join(filepath.Dir(path), "nil.sock"), nil); err == nil {
		t.Error("Listen accepted a nil handler")
	}
	if _, err := Dial(context.Background(), filepath.Join(filepath.Dir(path), "absent.sock")); err == nil {
		t.Error("Dial succeeded on a missing socket")
	}
}
