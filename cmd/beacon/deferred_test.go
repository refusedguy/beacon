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
	mu           sync.Mutex
	rows         map[string]store.DeferredWiring
	err          error
	calls        int
	doneCalls    int
	statusWrites int
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

// MarkDeferredDone applies the store's transition: todo or claimed moves to
// done once, and a repeat leaves the row unchanged. statusWrites counts the
// transitions that actually changed a row.
func (f *fakeDeferredDB) MarkDeferredDone(watchID, reason string) (store.DeferredWiring, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.DeferredWiring{}, f.err
	}
	f.doneCalls++
	k := f.key(watchID, reason)
	row, ok := f.rows[k]
	if !ok {
		return store.DeferredWiring{}, store.ErrDeferredNotFound
	}
	if row.Status != store.DeferredStatusDone {
		if row.Status != store.DeferredStatusTodo && row.Status != store.DeferredStatusClaimed {
			return store.DeferredWiring{}, store.ErrDeferredWrongStatus
		}
		row.Status = store.DeferredStatusDone
		f.rows[k] = row
		f.statusWrites++
	}
	return row, nil
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

// TestDeferredDispatchJSON proves `beacon deferred --format json` works through
// the real CLI dispatch, with no runner replacement and the durable store.
func TestDeferredDispatchJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(beaconStateDirEnv, dir)
	db, err := openProcessStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.DeferWiring(store.DeferredWiring{
		WatchID:    "ses_dispatch_watch",
		PR:         77,
		Title:      "dispatch card",
		Reason:     "needs wiring",
		OwnerFiles: []string{"cmd/beacon/main.go"},
	}); err != nil {
		t.Fatal(err)
	}

	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	c := &CLI{out: out, errOut: errOut}
	if code := c.Run([]string{"deferred", "--format", "json"}); code != 0 {
		t.Fatalf("beacon deferred code = %d, stderr = %q", code, errOut.String())
	}

	var cards []mcp.DeferredWiringCard
	if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
		t.Fatalf("JSON output: %v (%q)", err, out.String())
	}
	if len(cards) != 1 {
		t.Fatalf("карточек в JSON: %d, ожидалась 1", len(cards))
	}
	if cards[0].CardID != "WIRING-77" || cards[0].WatchID != "ses_dispatch_watch" ||
		cards[0].Status != store.DeferredStatusTodo {
		t.Fatalf("карточка = %+v", cards[0])
	}
}

func TestDeferredDoneMarksOnlyTheWatchRows(t *testing.T) {
	fake := newFakeDeferredDB()
	for _, in := range []store.DeferredWiring{
		{WatchID: "ses_done", PR: 120, Reason: "reason a"},
		{WatchID: "ses_done", PR: 121, Reason: "reason b"},
		{WatchID: "ses_other", PR: 122, Reason: "reason c"},
	} {
		if _, err := fake.DeferWiring(in); err != nil {
			t.Fatal(err)
		}
	}

	s := newDurableDeferredStore(fake)
	marked, err := s.MarkDone(context.Background(), "ses_done")
	if err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if marked != 2 {
		t.Fatalf("MarkDone = %d, ожидалось 2", marked)
	}

	rows, err := fake.ListDeferred("")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		want := store.DeferredStatusTodo
		if row.WatchID == "ses_done" {
			want = store.DeferredStatusDone
		}
		if row.Status != want {
			t.Fatalf("строка %q: статус %q, ожидался %q", row.WatchID, row.Status, want)
		}
	}
	if fake.statusWrites != 2 {
		t.Fatalf("изменений статуса: %d, ожидалось 2", fake.statusWrites)
	}
}

func TestDeferredDoneRepeatAndUnknownWatch(t *testing.T) {
	fake := newFakeDeferredDB()
	if _, err := fake.DeferWiring(store.DeferredWiring{
		WatchID: "ses_repeat", PR: 130, Reason: "reason",
	}); err != nil {
		t.Fatal(err)
	}

	s := newDurableDeferredStore(fake)
	ctx := context.Background()
	if _, err := s.MarkDone(ctx, "ses_repeat"); err != nil {
		t.Fatal(err)
	}
	writesAfterFirst := fake.statusWrites

	marked, err := s.MarkDone(ctx, "ses_repeat")
	if err != nil || marked != 1 {
		t.Fatalf("повтор: marked = %d, err = %v", marked, err)
	}
	if fake.statusWrites != writesAfterFirst {
		t.Fatalf("повтор изменил статус: %d -> %d", writesAfterFirst, fake.statusWrites)
	}

	if _, err := s.MarkDone(ctx, "ses_unknown"); !errors.Is(err, store.ErrDeferredNotFound) {
		t.Fatalf("неизвестный watchID: err = %v, ожидался ErrDeferredNotFound", err)
	}
	if fake.statusWrites != writesAfterFirst {
		t.Fatal("неизвестный watchID изменил store")
	}
}

func TestDeferredDoneCLIExitCodes(t *testing.T) {
	fake := newFakeDeferredDB()
	if _, err := fake.DeferWiring(store.DeferredWiring{
		WatchID: "ses_cli_done", PR: 140, Reason: "reason",
	}); err != nil {
		t.Fatal(err)
	}

	old := makeDeferredDeps
	makeDeferredDeps = func(*CLI) (deferredDeps, func() error, error) {
		s := newDurableDeferredStore(fake)
		return deferredDeps{Writer: s, Done: s}, func() error { return nil }, nil
	}
	defer func() { makeDeferredDeps = old }()

	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	c := &CLI{out: out, errOut: errOut}
	for _, args := range [][]string{
		{"done", "ses_cli_done"},
		{"done", "ses_cli_done"},
	} {
		if code := runDeferred(c, args); code != 0 {
			t.Fatalf("runDeferred(%v) = %d, stderr = %q", args, code, errOut.String())
		}
	}
	for _, args := range [][]string{
		{"done"},
		{"done", "ses_unknown"},
	} {
		if code := runDeferred(c, args); code != 2 {
			t.Fatalf("runDeferred(%v) = %d, ожидался 2, stderr = %q", args, code, errOut.String())
		}
	}
}

// TestDeferredDoneDispatchJSON pins the card's check: the done subcommand
// works through CLI dispatch and the list shows the new status in JSON.
func TestDeferredDoneDispatchJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(beaconStateDirEnv, dir)
	db, err := openProcessStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.DeferWiring(store.DeferredWiring{
		WatchID: "ses_dispatch_done",
		PR:      150,
		Title:   "dispatch card",
		Reason:  "needs wiring",
	}); err != nil {
		t.Fatal(err)
	}

	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	c := &CLI{out: out, errOut: errOut}
	if code := c.Run([]string{"deferred", "done", "ses_dispatch_done"}); code != 0 {
		t.Fatalf("beacon deferred done code = %d, stderr = %q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := c.Run([]string{"deferred", "--format", "json"}); code != 0 {
		t.Fatalf("beacon deferred --format json code = %d, stderr = %q", code, errOut.String())
	}

	var cards []mcp.DeferredWiringCard
	if err := json.Unmarshal(out.Bytes(), &cards); err != nil {
		t.Fatalf("JSON output: %v (%q)", err, out.String())
	}
	if len(cards) != 1 {
		t.Fatalf("карточек в JSON: %d, ожидалась 1", len(cards))
	}
	if cards[0].WatchID != "ses_dispatch_done" || cards[0].Status != store.DeferredStatusDone {
		t.Fatalf("карточка = %+v", cards[0])
	}
}
