package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/httpapi"
)

type w8bDecider struct {
	req   httpapi.ClientDecision
	calls int
	resp  httpapi.ClientDecisionReceipt
	err   error
}

func (d *w8bDecider) Decide(_ context.Context, req httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error) {
	d.calls++
	d.req = req
	return d.resp, d.err
}

type w8bStore struct {
	req   httpapi.ClientDecision
	calls int
	err   error
}

func (s *w8bStore) Apply(_ context.Context, req httpapi.ClientDecision) error {
	s.calls++
	s.req = req
	return s.err
}

// w8bHumanHarness replaces the three seams of the command and restores them
// after the test. It writes a connection.json only when asked to.
func w8bHumanHarness(t *testing.T, withConnection bool) (*CLI, *w8bDecider, *w8bStore) {
	t.Helper()
	dir := t.TempDir()
	if withConnection {
		data := []byte(`{"host":"127.0.0.1","port":8787,"token":"t"}`)
		if err := os.WriteFile(filepath.Join(dir, "connection.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, out, errOut, _ := w69CLI(t, nil)
	decider := &w8bDecider{resp: httpapi.ClientDecisionReceipt{OK: true, Applied: true}}
	store := &w8bStore{}
	prevDir, prevDecider, prevStore := humanStateDir, humanDeciderFor, humanStoreFor
	humanStateDir = func() (string, error) { return dir, nil }
	humanDeciderFor = func(httpapi.ClientConn) (humanDecider, error) { return decider, nil }
	humanStoreFor = func(string) humanDecisionStore { return store }
	t.Cleanup(func() {
		humanStateDir, humanDeciderFor, humanStoreFor = prevDir, prevDecider, prevStore
	})
	_ = errOut
	_ = out
	return c, decider, store
}

func TestW8BHumanRejectsUnknownVerb(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"ban", "w-1"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 0 || store.calls != 0 {
		t.Fatalf("unknown verb reached a write path: http=%d sqlite=%d", decider.calls, store.calls)
	}
}

func TestW8BHumanRequiresVerbAndTarget(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"approve"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 0 || store.calls != 0 {
		t.Fatalf("missing target reached a write path")
	}
}

func TestW8BHumanPostsToServer(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	if code := runHuman(c, []string{"--note", "ok by me", "approve", "w-1"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 1 || store.calls != 0 {
		t.Fatalf("http=%d sqlite=%d, want 1/0", decider.calls, store.calls)
	}
	if decider.req.Verb != "approve" || decider.req.Target != "w-1" || decider.req.Note != "ok by me" {
		t.Fatalf("request = %+v", decider.req)
	}
}

func TestW8BHumanFallsBackToSQLiteWithoutServer(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, false)
	if code := runHuman(c, []string{"unblock", "blocker-7"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if store.calls != 1 || store.req.Verb != "unblock" || store.req.Target != "blocker-7" {
		t.Fatalf("sqlite request = %+v, calls %d", store.req, store.calls)
	}
	if decider.calls != 0 {
		t.Fatalf("no connection.json must not dial: %d", decider.calls)
	}
}

func TestW8BHumanFallsBackOnTransportError(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	decider.err = httpapi.ErrNoServer
	if code := runHuman(c, []string{"reject", "w-2"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if decider.calls != 1 || store.calls != 1 {
		t.Fatalf("http=%d sqlite=%d, want 1/1", decider.calls, store.calls)
	}
}

func TestW8BHumanDoesNotFallbackOnServerRefusal(t *testing.T) {
	c, decider, store := w8bHumanHarness(t, true)
	decider.err = errors.New("not parked")
	if code := runHuman(c, []string{"reject", "w-2"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if store.calls != 0 {
		t.Fatalf("server refusal must not fall back: %d", store.calls)
	}
}

func TestW8BHumanReportsSQLiteFailure(t *testing.T) {
	c, _, store := w8bHumanHarness(t, false)
	store.err = errors.New("db locked")
	if code := runHuman(c, []string{"pause", "w-3"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestW8BHumanRegisteredInCatalog(t *testing.T) {
	for _, entry := range catalog {
		if entry.Name == "human" {
			if !strings.Contains(entry.Usage, "<watchID|blockerID>") {
				t.Fatalf("usage = %q", entry.Usage)
			}
			return
		}
	}
	t.Fatal("human missing from catalog")
}
