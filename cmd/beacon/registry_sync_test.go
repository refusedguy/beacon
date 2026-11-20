package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

func syncTestSessions(ids ...string) []opencode.Session {
	out := make([]opencode.Session, 0, len(ids))
	for _, id := range ids {
		out = append(out, opencode.Session{ID: id})
	}
	return out
}

func TestReconcileStaleMarksMissingSessions(t *testing.T) {
	entries := []registryEntry{
		{AgentID: "live", SessionID: "ses-live", Status: registryStatusActive},
		{AgentID: "gone", SessionID: "ses-gone", Status: registryStatusActive},
	}
	got := reconcileStale(entries, []string{"ses-live"})
	want := []registryStatusUpdate{{AgentID: "gone", Status: registryStatusStale}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}
}

func TestReconcileStaleClearsStaleForLiveSession(t *testing.T) {
	entries := []registryEntry{
		{AgentID: "back", SessionID: "ses-back", Status: registryStatusStale},
	}
	got := reconcileStale(entries, []string{"ses-back"})
	want := []registryStatusUpdate{{AgentID: "back", Status: registryStatusActive}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}
}

func TestReconcileStaleEmptySessionIDIsStale(t *testing.T) {
	entries := []registryEntry{{AgentID: "no-session", Status: registryStatusActive}}
	got := reconcileStale(entries, nil)
	want := []registryStatusUpdate{{AgentID: "no-session", Status: registryStatusStale}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}

	// An empty live id must not rescue an empty session_id.
	got = reconcileStale(entries, []string{"", "   "})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updates with blank live ids = %+v, want %+v", got, want)
	}
}

func TestReconcileStaleKeepsStopped(t *testing.T) {
	entries := []registryEntry{
		{AgentID: "stopped", SessionID: "ses-stopped", Status: registryStatusStopped},
	}
	if got := reconcileStale(entries, nil); len(got) != 0 {
		t.Fatalf("stopped entry got updates %+v, want none", got)
	}
	if got := reconcileStale(entries, []string{"ses-stopped"}); len(got) != 0 {
		t.Fatalf("stopped entry revived by sync: %+v", got)
	}
}

func TestReconcileStaleIsIdempotent(t *testing.T) {
	entries := []registryEntry{
		{AgentID: "a", SessionID: "ses-a", Status: registryStatusActive},
		{AgentID: "b", SessionID: "ses-b", Status: registryStatusActive},
	}
	live := []string{"ses-a"}
	first := reconcileStale(entries, live)
	for _, u := range first {
		for i := range entries {
			if entries[i].AgentID == u.AgentID {
				entries[i].Status = u.Status
			}
		}
	}
	if second := reconcileStale(entries, live); len(second) != 0 {
		t.Fatalf("second pass wrote %+v, want no changes", second)
	}
}

func TestSyncRegistryWritesOnlyChangedStatuses(t *testing.T) {
	entries := []registryEntry{
		{AgentID: "a", SessionID: "ses-a", Status: registryStatusActive},
		{AgentID: "b", SessionID: "ses-b", Status: registryStatusActive},
		{AgentID: "c", SessionID: "ses-c", Status: registryStatusStale},
		{AgentID: "d", SessionID: "ses-d", Status: registryStatusStopped},
	}
	type write struct{ agent, status string }
	var writes []write
	sourceCalls := 0
	source := registrySessionSourceFunc(func(context.Context) ([]opencode.Session, error) {
		sourceCalls++
		return syncTestSessions("ses-a", "ses-c"), nil
	})
	storePort := registryStoreFuncs{
		entries: func(projectKey string) ([]registryEntry, error) {
			if projectKey != "proj-1" {
				t.Fatalf("entries project = %q, want proj-1", projectKey)
			}
			return entries, nil
		},
		setStatus: func(projectKey, agent, status string) error {
			if projectKey != "proj-1" {
				t.Fatalf("setStatus project = %q, want proj-1", projectKey)
			}
			writes = append(writes, write{agent, status})
			return nil
		},
	}

	got, err := syncRegistry(context.Background(), source, storePort, "proj-1")
	if err != nil {
		t.Fatalf("syncRegistry: %v", err)
	}
	wantWrites := []write{{"b", registryStatusStale}, {"c", registryStatusActive}}
	if !reflect.DeepEqual(writes, wantWrites) {
		t.Fatalf("writes = %+v, want %+v", writes, wantWrites)
	}
	if sourceCalls != 1 {
		t.Fatalf("session source called %d times, want 1", sourceCalls)
	}
	if !reflect.DeepEqual(got.Active, []string{"a", "c"}) {
		t.Fatalf("active = %v, want [a c]", got.Active)
	}
	if !reflect.DeepEqual(got.Stale, []string{"b"}) {
		t.Fatalf("stale = %v, want [b]", got.Stale)
	}
	if !reflect.DeepEqual(got.Updated, []registryStatusUpdate{
		{AgentID: "b", Status: registryStatusStale},
		{AgentID: "c", Status: registryStatusActive},
	}) {
		t.Fatalf("updated = %+v", got.Updated)
	}
}

// TestRegistrySyncNeverStartsModel is the acceptance check for ADR 0095: the
// source port is read-only, so a comparison pass cannot post a prompt or create
// a session. The test fails if the pass reads the session list more than once
// or touches any registry row more than its status needs.
func TestRegistrySyncNeverStartsModel(t *testing.T) {
	entries := []registryEntry{{AgentID: "gone", SessionID: "ses-gone", Status: registryStatusActive}}
	sourceCalls := 0
	source := registrySessionSourceFunc(func(context.Context) ([]opencode.Session, error) {
		sourceCalls++
		return nil, nil
	})
	statusCalls := 0
	storePort := registryStoreFuncs{
		entries: func(string) ([]registryEntry, error) { return entries, nil },
		setStatus: func(_, agent, status string) error {
			statusCalls++
			if agent != "gone" || status != registryStatusStale {
				t.Fatalf("setStatus(%q, %q), want (gone, stale)", agent, status)
			}
			return nil
		},
	}
	if _, err := syncRegistry(context.Background(), source, storePort, "proj"); err != nil {
		t.Fatalf("syncRegistry: %v", err)
	}
	if sourceCalls != 1 {
		t.Fatalf("session list read %d times, want 1", sourceCalls)
	}
	if statusCalls != 1 {
		t.Fatalf("status writes = %d, want 1", statusCalls)
	}
}

func TestSyncRegistryPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	okSource := registrySessionSourceFunc(func(context.Context) ([]opencode.Session, error) { return nil, nil })
	okStore := registryStoreFuncs{
		entries:   func(string) ([]registryEntry, error) { return nil, nil },
		setStatus: func(string, string, string) error { return nil },
	}

	t.Run("source", func(t *testing.T) {
		source := registrySessionSourceFunc(func(context.Context) ([]opencode.Session, error) { return nil, boom })
		if _, err := syncRegistry(context.Background(), source, okStore, "proj"); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("entries", func(t *testing.T) {
		storePort := registryStoreFuncs{
			entries:   func(string) ([]registryEntry, error) { return nil, boom },
			setStatus: func(string, string, string) error { return nil },
		}
		if _, err := syncRegistry(context.Background(), okSource, storePort, "proj"); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("set status", func(t *testing.T) {
		storePort := registryStoreFuncs{
			entries: func(string) ([]registryEntry, error) {
				return []registryEntry{{AgentID: "a", SessionID: "gone", Status: registryStatusActive}}, nil
			},
			setStatus: func(string, string, string) error { return boom },
		}
		if _, err := syncRegistry(context.Background(), okSource, storePort, "proj"); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})
}

func TestSyncRegistryRejectsBadInputs(t *testing.T) {
	okSource := registrySessionSourceFunc(func(context.Context) ([]opencode.Session, error) { return nil, nil })
	okStore := registryStoreFuncs{
		entries:   func(string) ([]registryEntry, error) { return nil, nil },
		setStatus: func(string, string, string) error { return nil },
	}
	for _, tc := range []struct {
		name   string
		source registrySessionSource
		store  registryStore
		key    string
	}{
		{"nil source", nil, okStore, "proj"},
		{"nil store", okSource, nil, "proj"},
		{"empty key", okSource, okStore, "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := syncRegistry(context.Background(), tc.source, tc.store, tc.key); err == nil {
				t.Fatal("syncRegistry: want an error")
			}
		})
	}
}

func TestRegistryWakeAllowed(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{registryStatusActive, true},
		{registryStatusStale, false},
		{registryStatusStopped, false},
		{"", false},
		{"unknown", false},
	} {
		if got := registryWakeAllowed(tc.status); got != tc.want {
			t.Errorf("registryWakeAllowed(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestLiveSessionIDsDropsBlanks(t *testing.T) {
	sessions := append(syncTestSessions("ses-a", " ", "ses-b"), opencode.Session{ID: ""})
	got := liveSessionIDs(sessions)
	want := []string{"ses-a", "ses-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("liveSessionIDs = %v, want %v", got, want)
	}
}
