package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

// memoryPort records what the pure logic asks of the I/O boundary. It holds
// everything in memory, so no file or SQL driver is touched by these tests.
type memoryPort struct {
	openCalls  int
	openPath   string
	openErr    error
	execCalls  []string
	execErr    error
	appliedErr error
	applyErr   error
	applyCalls []string
	recorded   map[string]string
	closeCalls int
}

func newMemoryPort() *memoryPort {
	return &memoryPort{recorded: make(map[string]string)}
}

func (p *memoryPort) Open(_ context.Context, path string) error {
	p.openCalls++
	if p.openErr != nil {
		return p.openErr
	}
	p.openPath = path
	return nil
}

func (p *memoryPort) Exec(_ context.Context, statement string) error {
	p.execCalls = append(p.execCalls, statement)
	return p.execErr
}

func (p *memoryPort) Applied(_ context.Context) ([]string, error) {
	if p.appliedErr != nil {
		return nil, p.appliedErr
	}
	names := make([]string, 0, len(p.recorded))
	for name := range p.recorded {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (p *memoryPort) Apply(_ context.Context, name, body string) error {
	p.applyCalls = append(p.applyCalls, name)
	if p.applyErr != nil {
		return p.applyErr
	}
	p.recorded[name] = body
	return nil
}

func (p *memoryPort) Close() error {
	p.closeCalls++
	return nil
}

func TestOpenAppliesPragmasAndMigrations(t *testing.T) {
	p := newMemoryPort()
	c, err := Open(context.Background(), "/state/beacon", p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if c == nil {
		t.Fatal("Open returned a nil connection")
	}
	if c.Path() != "/state/beacon" {
		t.Fatalf("Path = %q, want /state/beacon", c.Path())
	}
	if p.openCalls != 1 || p.openPath != "/state/beacon" {
		t.Fatalf("open calls = %d, path = %q", p.openCalls, p.openPath)
	}
	if len(p.execCalls) < 3 {
		t.Fatalf("statements = %v, want two PRAGMAs then the ledger", p.execCalls)
	}
	if p.execCalls[0] != "PRAGMA journal_mode = WAL" {
		t.Fatalf("first statement = %q", p.execCalls[0])
	}
	if p.execCalls[1] != "PRAGMA foreign_keys = ON" {
		t.Fatalf("second statement = %q", p.execCalls[1])
	}
	if !strings.Contains(p.execCalls[2], "schema_migrations") {
		t.Fatalf("third statement = %q, want the migration ledger", p.execCalls[2])
	}
	body, ok := p.recorded["0001_applications"]
	if !ok || !strings.Contains(body, "CREATE TABLE IF NOT EXISTS applications") {
		t.Fatalf("applications migration = %q, recorded = %v", body, ok)
	}
}

func TestMigrateApplications(t *testing.T) {
	p := newMemoryPort()
	if err := migrate(context.Background(), p); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(p.recorded) != 1 {
		t.Fatalf("recorded migrations = %v, want one", p.recorded)
	}
	body, ok := p.recorded["0001_applications"]
	if !ok {
		t.Fatalf("applications migration not recorded: %v", p.recorded)
	}
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS applications", "id", "name", "created_at"} {
		if !strings.Contains(body, want) {
			t.Fatalf("applications body missing %q: %s", want, body)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	p := newMemoryPort()
	if _, err := Open(ctx, "/state/beacon", p); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstApplied := len(p.applyCalls)
	if firstApplied == 0 {
		t.Fatal("first Open applied no migration")
	}

	if _, err := Open(ctx, "/state/beacon", p); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if len(p.applyCalls) != firstApplied {
		t.Fatalf("apply calls after second Open = %v, want %v", p.applyCalls, firstApplied)
	}
	if len(p.recorded) != firstApplied {
		t.Fatalf("recorded migrations after second Open = %v", p.recorded)
	}
}

func TestOpenReportsPortErrors(t *testing.T) {
	ctx := context.Background()

	openFailure := errors.New("cannot create state dir")
	p := newMemoryPort()
	p.openErr = openFailure
	c, err := Open(ctx, "/state/beacon", p)
	if c != nil {
		t.Fatal("Open returned a connection after an open error")
	}
	if !errors.Is(err, openFailure) {
		t.Fatalf("error = %v, want open failure", err)
	}
	if len(p.execCalls) != 0 || len(p.applyCalls) != 0 {
		t.Fatalf("open error still reached the schema: exec=%v apply=%v", p.execCalls, p.applyCalls)
	}

	pragmaFailure := errors.New("pragma failed")
	p2 := newMemoryPort()
	p2.execErr = pragmaFailure
	c2, err := Open(ctx, "/state/beacon", p2)
	if c2 != nil {
		t.Fatal("Open returned a connection after a pragma error")
	}
	if !errors.Is(err, pragmaFailure) {
		t.Fatalf("error = %v, want pragma failure", err)
	}
	if p2.closeCalls != 1 || len(p2.applyCalls) != 0 {
		t.Fatalf("pragma error: closes = %d, applies = %v", p2.closeCalls, p2.applyCalls)
	}

	applyFailure := errors.New("migration failed")
	p3 := newMemoryPort()
	p3.applyErr = applyFailure
	c3, err := Open(ctx, "/state/beacon", p3)
	if c3 != nil {
		t.Fatal("Open returned a connection after a migration error")
	}
	if !errors.Is(err, applyFailure) {
		t.Fatalf("error = %v, want migration failure", err)
	}
	if p3.closeCalls != 1 {
		t.Fatalf("close calls after migration error = %d, want 1", p3.closeCalls)
	}
}

func TestOpenRejectsEmptyStateDir(t *testing.T) {
	p := newMemoryPort()
	c, err := Open(context.Background(), "", p)
	if c != nil {
		t.Fatal("Open returned a connection for an empty state dir")
	}
	if !errors.Is(err, ErrEmptyStateDir) {
		t.Fatalf("error = %v, want ErrEmptyStateDir", err)
	}
	if p.openCalls != 0 || len(p.execCalls) != 0 || len(p.applyCalls) != 0 {
		t.Fatalf("empty dir touched the port: open=%d exec=%v apply=%v",
			p.openCalls, p.execCalls, p.applyCalls)
	}
}

func TestOpenRejectsNilPort(t *testing.T) {
	c, err := Open(context.Background(), "/state/beacon", nil)
	if c != nil || err == nil {
		t.Fatalf("nil port: connection = %v, error = %v", c, err)
	}
}
