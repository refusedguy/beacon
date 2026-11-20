package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

// fakeDeferredDB keeps the store's first-write-wins rule in memory. The
// adapter tests use it to check card creation, idempotency and error
// propagation without a database.
type fakeDeferredDB struct {
	mu    sync.Mutex
	rows  map[string]store.DeferredWiring
	err   error
	calls int
}

func newFakeDeferredDB() *fakeDeferredDB {
	return &fakeDeferredDB{rows: make(map[string]store.DeferredWiring)}
}

func (f *fakeDeferredDB) key(watchID, reason string) string {
	return watchID + "\x00" + reason
}

func (f *fakeDeferredDB) DeferWiring(in store.DeferredWiring) (store.DeferredWiring, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return store.DeferredWiring{}, f.err
	}
	if f.rows == nil {
		f.rows = make(map[string]store.DeferredWiring)
	}
	k := f.key(in.WatchID, in.Reason)
	if row, ok := f.rows[k]; ok {
		return row, nil
	}
	if in.Status == "" {
		in.Status = store.DeferredStatusTodo
	}
	f.rows[k] = in
	return in, nil
}

func (f *fakeDeferredDB) ListDeferred(status string) ([]store.DeferredWiring, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]store.DeferredWiring, 0, len(f.rows))
	for _, row := range f.rows {
		if status != "" && row.Status != status {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

var _ deferredStoreDB = (*fakeDeferredDB)(nil)

func TestDurableDeferredStoreCreatesAndIsIdempotent(t *testing.T) {
	fake := newFakeDeferredDB()
	s := newDurableDeferredStore(fake)
	ctx := context.Background()

	card := mcp.DeferredWiringCard{
		CardID:     "WIRING-77",
		WatchID:    "ses_watch_77",
		PR:         77,
		Title:      "wire the adapter",
		Reason:     "writer was nil",
		OwnerFiles: []string{"cmd/beacon/mcp.go"},
		Status:     mcp.DeferredStatusDeferred,
	}
	created, err := s.DeferWiring(ctx, card)
	if err != nil {
		t.Fatalf("DeferWiring: %v", err)
	}
	if !created {
		t.Fatal("первая запись должна сообщить created=true")
	}

	repeat := card
	repeat.Title = "different title"
	repeat.OwnerFiles = []string{"other.go"}
	created, err = s.DeferWiring(ctx, repeat)
	if err != nil {
		t.Fatalf("DeferWiring repeat: %v", err)
	}
	if created {
		t.Fatal("повтор по (watchID, reason) не должен создавать новую карточку")
	}
	if fake.calls != 2 {
		t.Fatalf("вызовов store.DeferWiring: %d, ожидалось 2", fake.calls)
	}

	cards, err := s.DeferredWirings(ctx)
	if err != nil {
		t.Fatalf("DeferredWirings: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("карточек: %d, ожидалась 1", len(cards))
	}
	want := mcp.DeferredWiringCard{
		CardID:     "WIRING-77",
		WatchID:    "ses_watch_77",
		PR:         77,
		Title:      "wire the adapter",
		Reason:     "writer was nil",
		OwnerFiles: []string{"cmd/beacon/mcp.go"},
		Status:     store.DeferredStatusTodo,
	}
	if !reflect.DeepEqual(cards[0], want) {
		t.Fatalf("карточка = %+v, ожидалась %+v", cards[0], want)
	}
}

func TestDurableDeferredStorePropagatesStoreError(t *testing.T) {
	boom := errors.New("store unavailable")
	fake := newFakeDeferredDB()
	fake.err = boom
	s := newDurableDeferredStore(fake)
	ctx := context.Background()
	card := mcp.DeferredWiringCard{
		WatchID: "ses_error_watch",
		PR:      12,
		Reason:  "disk failure",
	}

	if _, err := s.DeferWiring(ctx, card); !errors.Is(err, boom) {
		t.Fatalf("DeferWiring error = %v, ожидался %v", err, boom)
	}
	if _, err := s.DeferredWirings(ctx); !errors.Is(err, boom) {
		t.Fatalf("DeferredWirings error = %v, ожидался %v", err, boom)
	}
}

// TestBuildMCPDependenciesWiresDeferWiring pins the production fix: the MCP
// dependencies carry a non-nil writer that persists a card to the same store.
func TestBuildMCPDependenciesWiresDeferWiring(t *testing.T) {
	dir := t.TempDir()
	db, err := openProcessStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	deps := buildMCPDependencies(db, dir, nil)
	defer stopVerdictSource(deps.Source)
	if deps.DeferWiring == nil {
		t.Fatal("production dependencies оставили DeferWiring nil")
	}

	card := mcp.DeferredWiringCard{
		WatchID:    "ses_prod_watch",
		PR:         91,
		Title:      "production port",
		Reason:     "nil writer",
		OwnerFiles: []string{"cmd/beacon/mcp.go"},
	}
	created, err := deps.DeferWiring.DeferWiring(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("первая запись через production port должна создавать карточку")
	}

	rows, err := db.ListDeferred("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("строк deferred_wiring: %d, ожидалась 1", len(rows))
	}
	if rows[0].WatchID != card.WatchID || rows[0].Reason != card.Reason || rows[0].PR != card.PR {
		t.Fatalf("строка = %+v, ожидалась карточка %+v", rows[0], card)
	}
}

// TestDeferredCLIPortReadsDurableStore proves the read side of beacon deferred
// uses the same durable rows the MCP tool writes.
func TestDeferredCLIPortReadsDurableStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(beaconStateDirEnv, dir)
	db, err := openProcessStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.DeferWiring(store.DeferredWiring{
		WatchID:    "ses_cli_watch",
		PR:         55,
		Title:      "cli card",
		Reason:     "needs wiring",
		OwnerFiles: []string{"cmd/beacon/mcp.go"},
	}); err != nil {
		t.Fatal(err)
	}

	old := makeDeferredDeps
	makeDeferredDeps = defaultDeferredDeps
	defer func() { makeDeferredDeps = old }()

	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	c := &CLI{out: out, errOut: errOut}
	if code := runDeferred(c, []string{"--format", "json"}); code != 0 {
		t.Fatalf("runDeferred code = %d, stderr = %q", code, errOut.String())
	}

	var cards []mcp.DeferredWiringCard
	if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
		t.Fatalf("JSON output: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("карточек в JSON: %d, ожидалась 1", len(cards))
	}
	if cards[0].CardID != "WIRING-55" || cards[0].WatchID != "ses_cli_watch" ||
		cards[0].Status != store.DeferredStatusTodo {
		t.Fatalf("карточка = %+v", cards[0])
	}
}
