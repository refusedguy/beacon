package main

// BCX-58. A live registry boundary check with two projects in one server.
//
// The test drives the real opencode HTTP client against a session.list
// endpoint, writes registrations to a real SQLite store, runs the production
// sync and reads the wake guard. It starts no model and sends no wake: the
// session source has no prompt method, and a stale status is denied at the
// guard. The two projects share one store and one server, but use different
// watch ids. The agent name "shared" is deliberately present in both projects.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// liveRegistrySessionList is the GET /api/session payload. Only project A has
// live sessions; project B's sessions have ended, so its rows must go stale on
// its own sync and stay untouched on project A's sync.
const liveRegistrySessionList = `{
  "data": [
    {"id": "ses-watch-a", "location": {"directory": "/worktrees/project-a"}},
    {"id": "ses-shared-a", "location": {"directory": "/worktrees/project-a"}}
  ]
}`

type liveAgent struct {
	id      string
	session string
}

type liveProject struct {
	key     string
	watchID string
	agents  []liveAgent
}

func TestLiveRegistryTwoProjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, liveRegistrySessionList)
	}))
	defer server.Close()

	client, err := opencode.New(opencode.Config{URL: server.URL}, nil)
	if err != nil {
		t.Fatalf("opencode.New: %v", err)
	}
	source := registrySessionSourceFunc(func(ctx context.Context) ([]opencode.Session, error) {
		return client.Sessions(ctx)
	})

	db, err := store.Open(filepath.Join(t.TempDir(), "registry-live.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	projects := []liveProject{
		{
			key:     "project-live-a",
			watchID: "watch-live-a",
			agents: []liveAgent{
				{id: "agent-a", session: "ses-watch-a"},
				{id: "shared", session: "ses-shared-a"},
				{id: "gone-a", session: "ses-missing-a"},
			},
		},
		{
			key:     "project-live-b",
			watchID: "watch-live-b",
			agents: []liveAgent{
				{id: "agent-b", session: "ses-watch-b"},
				{id: "shared", session: "ses-shared-b"},
			},
		},
	}

	for _, p := range projects {
		for _, a := range p.agents {
			_, err := db.RegisterExecutor(
				store.ExecutorRecord{
					ProjectKey: p.key,
					AgentID:    a.id,
					SessionID:  a.session,
					Status:     store.RegistryStatusActive,
				},
				store.WatchBinding{
					WatchID:    p.watchID + "-" + a.id,
					Revision:   1,
					ProjectKey: p.key,
					AgentID:    a.id,
				},
			)
			if err != nil {
				t.Fatalf("RegisterExecutor(%s/%s): %v", p.key, a.id, err)
			}
		}
	}

	ctx := context.Background()
	port := registryDBStore{db: db}

	resA, err := syncRegistry(ctx, source, port, projects[0].key)
	if err != nil {
		t.Fatalf("syncRegistry(project A): %v", err)
	}
	if resA.ProjectKey != projects[0].key {
		t.Fatalf("project A result key = %q", resA.ProjectKey)
	}
	if !reflect.DeepEqual(resA.Active, []string{"agent-a", "shared"}) {
		t.Fatalf("project A active = %v, want [agent-a shared]", resA.Active)
	}
	if !reflect.DeepEqual(resA.Stale, []string{"gone-a"}) {
		t.Fatalf("project A stale = %v, want [gone-a]", resA.Stale)
	}

	// Syncing A must not mark project B's ended sessions stale. Only B's own
	// pass owns its rows, even though both projects read the same server.
	bSharedBefore, found, err := db.Executor(projects[1].key, "shared")
	if err != nil || !found {
		t.Fatalf("read project B shared before its sync: found=%v err=%v", found, err)
	}
	if bSharedBefore.Status != store.RegistryStatusActive {
		t.Fatalf("project B shared status after A sync = %q, want active", bSharedBefore.Status)
	}

	resB, err := syncRegistry(ctx, source, port, projects[1].key)
	if err != nil {
		t.Fatalf("syncRegistry(project B): %v", err)
	}
	if resB.ProjectKey != projects[1].key {
		t.Fatalf("project B result key = %q", resB.ProjectKey)
	}
	if !reflect.DeepEqual(resB.Stale, []string{"agent-b", "shared"}) {
		t.Fatalf("project B stale = %v, want [agent-b shared]", resB.Stale)
	}
	if len(resB.Active) != 0 {
		t.Fatalf("project B active = %v, want none", resB.Active)
	}

	// The live observation carries the session worktree into the stored row.
	observed, found, err := db.Executor(projects[0].key, "agent-a")
	if err != nil || !found {
		t.Fatalf("read project A agent: found=%v err=%v", found, err)
	}
	if observed.Status != store.RegistryStatusActive {
		t.Fatalf("project A agent status = %q, want active", observed.Status)
	}
	if observed.Worktree != "/worktrees/project-a" {
		t.Fatalf("project A agent worktree = %q, want live session directory", observed.Worktree)
	}
	if observed.LastSeen <= 0 {
		t.Fatalf("project A agent last_seen = %d, want a live observation", observed.LastSeen)
	}

	// The wake guard is project bound. A's "shared" stays active while B's
	// "shared" is stale, so a name collision cannot make B wakeable through A.
	for _, tc := range []struct {
		name      string
		project   string
		target    string
		wantAllow bool
	}{
		{"own active by agent id", projects[0].key, "shared", true},
		{"own active by session id", projects[0].key, "ses-shared-a", true},
		{"own stale", projects[0].key, "gone-a", false},
		{"other project session", projects[0].key, "ses-shared-b", false},
		{"other project stale same name", projects[1].key, "shared", false},
	} {
		status, allowed := newWakeRegistryGuard(db, tc.project).WakeStatus(tc.target)
		if allowed != tc.wantAllow {
			t.Errorf("%s: WakeStatus(%q) allowed=%v status=%q, want %v",
				tc.name, tc.target, allowed, status, tc.wantAllow)
		}
	}

	// A repeat pass writes nothing: status changes are idempotent.
	again, err := syncRegistry(ctx, source, port, projects[0].key)
	if err != nil {
		t.Fatalf("repeat syncRegistry(project A): %v", err)
	}
	if len(again.Updated) != 0 {
		t.Fatalf("repeat sync wrote %+v, want no changes", again.Updated)
	}
}
