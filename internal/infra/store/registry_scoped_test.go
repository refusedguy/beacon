package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// seedScopedRegistry writes two projects into one database. "shared" and
// "shared-watch" use the same identifier in both projects, so a query that
// forgets project_key would return the wrong row. Rows are inserted directly
// because the migration schema (T5.21) and the registry.go upsert (T5.22)
// currently disagree on the runtime column; this card only reads.
func seedScopedRegistry(t *testing.T) *DB {
	t.Helper()
	d := openTestDB(t, "scoped.db")
	now := time.Now().UTC().UnixNano()
	executors := []struct{ project, agent, target string }{
		{"project-a", "agent-a", "target-a"},
		{"project-a", "shared", "target-a-shared"},
		{"project-b", "agent-b", "target-b"},
		{"project-b", "shared", "target-b-shared"},
	}
	bindings := []struct {
		watch, project, agent string
		revision              uint64
	}{
		{"watch-a", "project-a", "agent-a", 1},
		{"watch-b", "project-b", "agent-b", 1},
		{"shared-watch", "project-b", "agent-b", 1},
	}
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		for _, e := range executors {
			_, err := tx.Exec(`INSERT INTO agent_registry
				(project_key, agent_id, role, runtime, status, session_id,
				 notify_adapter, notify_target, config_id, digest,
				 capabilities, created_at, updated_at)
				VALUES (?, ?, 'executor', 'opencode', 'active', ?,
				        'opencode', ?, 'ci=stub', 'dig', '["go"]', ?, ?)`,
				e.project, e.agent, e.agent, e.target, now, now)
			if err != nil {
				return err
			}
		}
		for _, b := range bindings {
			_, err := tx.Exec(`INSERT INTO watch_bindings
				(project_key, watch_id, revision, agent_id, config_id,
				 fencing_token, subscriber_id, created_at)
				VALUES (?, ?, ?, ?, 'ci=stub', ?, ?, ?)`,
				b.project, b.watch, int64(b.revision), b.agent,
				int64(b.revision), b.agent, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed scoped registry: %v", err)
	}
	return d
}

func scopedRegistry(t *testing.T, d *DB, project string) *ScopedRegistry {
	t.Helper()
	scope, err := NewProjectScope(project)
	if err != nil {
		t.Fatalf("NewProjectScope(%q): %v", project, err)
	}
	r, err := NewScopedRegistry(d, scope)
	if err != nil {
		t.Fatalf("NewScopedRegistry: %v", err)
	}
	return r
}

func wantInvalidInput(t *testing.T, err error) {
	t.Helper()
	var app *domain.AppError
	if !errors.As(err, &app) || app.Code != domain.CodeInvalidInput {
		t.Fatalf("error = %v, want AppError with CodeInvalidInput", err)
	}
}

func TestNewProjectScopeRejectsEmptyKey(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		scope, err := NewProjectScope(raw)
		if err == nil {
			t.Fatalf("NewProjectScope(%q) unexpectedly succeeded with key %q", raw, scope.Key())
		}
		wantInvalidInput(t, err)
	}
}

func TestNewScopedRegistryRejectsEmptyOrNil(t *testing.T) {
	d := openTestDB(t, "scoped.db")

	if _, err := NewScopedRegistry(d, ProjectScope{}); err == nil {
		t.Fatal("NewScopedRegistry accepted a zero scope")
	} else {
		wantInvalidInput(t, err)
	}
	scope, err := NewProjectScope("project-a")
	if err != nil {
		t.Fatalf("NewProjectScope: %v", err)
	}
	if _, err := NewScopedRegistry(nil, scope); err == nil {
		t.Fatal("NewScopedRegistry accepted a nil database")
	} else {
		wantInvalidInput(t, err)
	}

	// A caller in this package can build the struct literal directly. Every
	// read must still refuse it instead of falling back to an all-project scan.
	bad := &ScopedRegistry{db: d, scope: ProjectScope{}}
	if _, _, err := bad.Executor("agent-a"); err == nil {
		t.Fatal("Executor accepted a zero scope")
	} else {
		wantInvalidInput(t, err)
	}
	if _, err := bad.Executors(); err == nil {
		t.Fatal("Executors accepted a zero scope")
	} else {
		wantInvalidInput(t, err)
	}
	if _, err := bad.ExecutorCount(); err == nil {
		t.Fatal("ExecutorCount accepted a zero scope")
	} else {
		wantInvalidInput(t, err)
	}
	if _, _, err := bad.WatchBinding("watch-a", 1); err == nil {
		t.Fatal("WatchBinding accepted a zero scope")
	} else {
		wantInvalidInput(t, err)
	}
	if _, err := bad.WatchBindingCount("watch-a"); err == nil {
		t.Fatal("WatchBindingCount accepted a zero scope")
	} else {
		wantInvalidInput(t, err)
	}
}

func TestScopedExecutorDoesNotSeeOtherProject(t *testing.T) {
	d := seedScopedRegistry(t)
	a := scopedRegistry(t, d, "project-a")

	got, found, err := a.Executor("shared")
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	if !found {
		t.Fatal("shared executor of project A not found")
	}
	if got.ProjectKey != "project-a" || got.NotifyTarget != "target-a-shared" {
		t.Fatalf("Executor returned wrong project row: %+v", got)
	}

	if _, found, err := a.Executor("agent-b"); err != nil {
		t.Fatalf("Executor(agent-b): %v", err)
	} else if found {
		t.Fatal("project A saw an executor that belongs only to project B")
	}
}

func TestScopedExecutorsAndCountAreProjectLocal(t *testing.T) {
	d := seedScopedRegistry(t)
	a := scopedRegistry(t, d, "project-a")

	list, err := a.Executors()
	if err != nil {
		t.Fatalf("Executors: %v", err)
	}
	var agents []string
	for _, e := range list {
		if e.ProjectKey != "project-a" {
			t.Fatalf("Executors returned row from project %q", e.ProjectKey)
		}
		agents = append(agents, e.AgentID)
	}
	if want := []string{"agent-a", "shared"}; !reflect.DeepEqual(agents, want) {
		t.Fatalf("agents = %v, want %v", agents, want)
	}

	n, err := a.ExecutorCount()
	if err != nil {
		t.Fatalf("ExecutorCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("ExecutorCount = %d, want 2", n)
	}
}

func TestScopedWatchBindingDoesNotSeeOtherProject(t *testing.T) {
	d := seedScopedRegistry(t)
	a := scopedRegistry(t, d, "project-a")

	got, found, err := a.WatchBinding("watch-a", 1)
	if err != nil {
		t.Fatalf("WatchBinding: %v", err)
	}
	if !found {
		t.Fatal("watch-a of project A not found")
	}
	if got.ProjectKey != "project-a" || got.AgentID != "agent-a" {
		t.Fatalf("WatchBinding returned wrong project row: %+v", got)
	}

	// The binding for shared-watch belongs to project B. The underlying
	// registry.go read is keyed by watch id only, so the project predicate in
	// the scoped read is what keeps it invisible here.
	if _, found, err := a.WatchBinding("shared-watch", 1); err != nil {
		t.Fatalf("WatchBinding(shared-watch): %v", err)
	} else if found {
		t.Fatal("project A saw a watch binding that belongs only to project B")
	}
	if _, found, err := a.LatestWatchBinding("shared-watch"); err != nil {
		t.Fatalf("LatestWatchBinding(shared-watch): %v", err)
	} else if found {
		t.Fatal("project A saw the latest binding of project B")
	}
	if n, err := a.WatchBindingCount("shared-watch"); err != nil {
		t.Fatalf("WatchBindingCount(shared-watch): %v", err)
	} else if n != 0 {
		t.Fatalf("project A counted %d bindings of project B's watch", n)
	}

	b := scopedRegistry(t, d, "project-b")
	if n, err := b.WatchBindingCount("shared-watch"); err != nil {
		t.Fatalf("project B WatchBindingCount: %v", err)
	} else if n != 1 {
		t.Fatalf("project B WatchBindingCount = %d, want 1", n)
	}
	if got, found, err := b.LatestWatchBinding("shared-watch"); err != nil {
		t.Fatalf("project B LatestWatchBinding: %v", err)
	} else if !found {
		t.Fatal("project B did not find its own binding")
	} else if got.ProjectKey != "project-b" {
		t.Fatalf("latest binding has project key %q", got.ProjectKey)
	}
}

func TestExplicitProjectStillGoesThroughScope(t *testing.T) {
	d := seedScopedRegistry(t)

	// An explicit --project still resolves to a project key. Building the
	// scope from that key must not widen the read to other projects.
	explicit := scopedRegistry(t, d, "project-b")
	list, err := explicit.Executors()
	if err != nil {
		t.Fatalf("Executors: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("explicit project B returned %d executors, want 2", len(list))
	}
	for _, e := range list {
		if e.ProjectKey != "project-b" {
			t.Fatalf("explicit project leaked row from %q", e.ProjectKey)
		}
	}
	if _, found, err := explicit.WatchBinding("watch-a", 1); err != nil {
		t.Fatalf("WatchBinding(watch-a): %v", err)
	} else if found {
		t.Fatal("explicit project B saw project A's watch binding")
	}

	// An empty explicit project has no key to derive and is refused.
	if _, err := NewProjectScope(""); err == nil {
		t.Fatal("empty explicit project produced a scope")
	} else {
		wantInvalidInput(t, err)
	}

	// A wildcard-like string is an ordinary key, not an "all projects" mode.
	wild := scopedRegistry(t, d, "*")
	if list, err := wild.Executors(); err != nil {
		t.Fatalf("wildcard Executors: %v", err)
	} else if len(list) != 0 {
		t.Fatalf("wildcard scope returned %d rows, want 0", len(list))
	}
}

func TestScopedReadsRejectEmptyIdentifiers(t *testing.T) {
	d := seedScopedRegistry(t)
	r := scopedRegistry(t, d, "project-a")

	if _, _, err := r.Executor("  "); err == nil {
		t.Fatal("Executor accepted an empty agent id")
	} else {
		wantInvalidInput(t, err)
	}
	if _, _, err := r.WatchBinding("", 1); err == nil {
		t.Fatal("WatchBinding accepted an empty watch id")
	} else {
		wantInvalidInput(t, err)
	}
	if _, _, err := r.LatestWatchBinding(" "); err == nil {
		t.Fatal("LatestWatchBinding accepted an empty watch id")
	} else {
		wantInvalidInput(t, err)
	}
	if _, err := r.WatchBindingCount(""); err == nil {
		t.Fatal("WatchBindingCount accepted an empty watch id")
	} else {
		wantInvalidInput(t, err)
	}
}
