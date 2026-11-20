package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func sampleDeferred(watchID, reason string) DeferredWiring {
	return DeferredWiring{
		WatchID:    watchID,
		PR:         412,
		Title:      "wire reviewer",
		Reason:     reason,
		OwnerFiles: []string{"internal/infra/store/deferred.go", "cmd/beacon/main.go"},
		CreatedAt:  1000,
	}
}

func openDeferredTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func deferredCount(t *testing.T, d *DB) int {
	t.Helper()
	ok, err := tableExists(context.Background(), d.readDB, "deferred_wiring")
	if err != nil {
		t.Fatalf("tableExists: %v", err)
	}
	if !ok {
		return 0
	}
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM deferred_wiring`).Scan(&n); err != nil {
		t.Fatalf("count deferred rows: %v", err)
	}
	return n
}

func deferredIDs(rows []DeferredWiring) []int64 {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// TestDeferWiringRepeatIsIdempotent proves a second defer with the same
// (watch_id, reason) returns the first row, even with a different body, and
// writes no second row.
func TestDeferWiringRepeatIsIdempotent(t *testing.T) {
	d := openDeferredTestDB(t)

	want := sampleDeferred("watch-1", "stale subscriber")
	first, err := d.DeferWiring(want)
	if err != nil {
		t.Fatalf("first DeferWiring: %v", err)
	}
	if first.Status != DeferredStatusTodo {
		t.Fatalf("first status = %q, want %q", first.Status, DeferredStatusTodo)
	}
	if !reflect.DeepEqual(first.OwnerFiles, want.OwnerFiles) {
		t.Fatalf("owner_files round trip = %v, want %v", first.OwnerFiles, want.OwnerFiles)
	}

	repeat := sampleDeferred("watch-1", "stale subscriber")
	repeat.PR = 999
	repeat.Title = "changed title"
	repeat.OwnerFiles = []string{"other.go"}
	repeat.CreatedAt = 2000
	second, err := d.DeferWiring(repeat)
	if err != nil {
		t.Fatalf("repeat DeferWiring: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("repeat DeferWiring = %+v, want stored %+v", second, first)
	}
	if n := deferredCount(t, d); n != 1 {
		t.Fatalf("row count after repeat = %d, want 1", n)
	}
}

// TestListDeferredByStatus proves the status filter selects only the matching
// rows and that an empty status lists all of them.
func TestListDeferredByStatus(t *testing.T) {
	d := openDeferredTestDB(t)

	a, err := d.DeferWiring(sampleDeferred("watch-a", "reason-a"))
	if err != nil {
		t.Fatalf("defer a: %v", err)
	}
	b, err := d.DeferWiring(sampleDeferred("watch-b", "reason-b"))
	if err != nil {
		t.Fatalf("defer b: %v", err)
	}
	c, err := d.DeferWiring(sampleDeferred("watch-c", "reason-c"))
	if err != nil {
		t.Fatalf("defer c: %v", err)
	}
	if _, err := d.ClaimDeferred("watch-b", "reason-b"); err != nil {
		t.Fatalf("claim b: %v", err)
	}
	if _, err := d.MarkDeferredDone("watch-c", "reason-c"); err != nil {
		t.Fatalf("done c: %v", err)
	}

	cases := []struct {
		status string
		want   []int64
	}{
		{DeferredStatusTodo, []int64{a.ID}},
		{DeferredStatusClaimed, []int64{b.ID}},
		{DeferredStatusDone, []int64{c.ID}},
		{"", []int64{a.ID, b.ID, c.ID}},
	}
	for _, tc := range cases {
		rows, err := d.ListDeferred(tc.status)
		if err != nil {
			t.Fatalf("ListDeferred(%q): %v", tc.status, err)
		}
		if got := deferredIDs(rows); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ListDeferred(%q) ids = %v, want %v", tc.status, got, tc.want)
		}
	}

	if _, err := d.ListDeferred("bogus"); err == nil {
		t.Fatal("ListDeferred with unknown status succeeded, want invalid input")
	}
}

// TestClaimAndDoneChangeStatus proves the todo -> claimed -> done transitions
// persist, are idempotent on repeat, and reject finishing an already done item.
func TestClaimAndDoneChangeStatus(t *testing.T) {
	d := openDeferredTestDB(t)

	created, err := d.DeferWiring(sampleDeferred("watch-1", "reason-1"))
	if err != nil {
		t.Fatalf("DeferWiring: %v", err)
	}

	claimed, err := d.ClaimDeferred("watch-1", "reason-1")
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	if claimed.ID != created.ID || claimed.Status != DeferredStatusClaimed {
		t.Fatalf("claimed row = %+v, want id %d and status %q", claimed, created.ID, DeferredStatusClaimed)
	}
	claimedAgain, err := d.ClaimDeferred("watch-1", "reason-1")
	if err != nil {
		t.Fatalf("repeat ClaimDeferred: %v", err)
	}
	if !reflect.DeepEqual(claimedAgain, claimed) {
		t.Fatalf("repeat ClaimDeferred = %+v, want %+v", claimedAgain, claimed)
	}

	done, err := d.MarkDeferredDone("watch-1", "reason-1")
	if err != nil {
		t.Fatalf("MarkDeferredDone: %v", err)
	}
	if done.ID != created.ID || done.Status != DeferredStatusDone {
		t.Fatalf("done row = %+v, want id %d and status %q", done, created.ID, DeferredStatusDone)
	}
	doneAgain, err := d.MarkDeferredDone("watch-1", "reason-1")
	if err != nil {
		t.Fatalf("repeat MarkDeferredDone: %v", err)
	}
	if !reflect.DeepEqual(doneAgain, done) {
		t.Fatalf("repeat MarkDeferredDone = %+v, want %+v", doneAgain, done)
	}

	if _, err := d.ClaimDeferred("watch-1", "reason-1"); !errors.Is(err, ErrDeferredWrongStatus) {
		t.Fatalf("claim on a done row = %v, want ErrDeferredWrongStatus", err)
	}
	if n := deferredCount(t, d); n != 1 {
		t.Fatalf("row count after transitions = %d, want 1", n)
	}
}

// TestDeferredEmptyDB proves an untouched file has no phantom rows: listing
// returns nothing, and transitions on a missing key report not found.
func TestDeferredEmptyDB(t *testing.T) {
	d := openDeferredTestDB(t)

	for _, status := range []string{"", DeferredStatusTodo, DeferredStatusClaimed, DeferredStatusDone} {
		rows, err := d.ListDeferred(status)
		if err != nil {
			t.Fatalf("ListDeferred(%q) on empty DB: %v", status, err)
		}
		if len(rows) != 0 {
			t.Fatalf("ListDeferred(%q) on empty DB = %d rows, want 0", status, len(rows))
		}
	}
	if n := deferredCount(t, d); n != 0 {
		t.Fatalf("empty DB row count = %d, want 0", n)
	}
	if _, err := d.ClaimDeferred("missing", "reason"); !errors.Is(err, ErrDeferredNotFound) {
		t.Fatalf("claim missing = %v, want ErrDeferredNotFound", err)
	}
	if _, err := d.MarkDeferredDone("missing", "reason"); !errors.Is(err, ErrDeferredNotFound) {
		t.Fatalf("done missing = %v, want ErrDeferredNotFound", err)
	}
}

// TestDeferWiringConcurrentSameKey proves parallel defers of the same identity
// converge on one row and every caller receives that row's ID.
func TestDeferWiringConcurrentSameKey(t *testing.T) {
	d := openDeferredTestDB(t)

	const writers = 16
	var wg sync.WaitGroup
	ids := make(chan int64, writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := sampleDeferred("watch-concurrent", "same reason")
			in.PR = i
			in.Title = fmt.Sprintf("title-%d", i)
			row, err := d.DeferWiring(in)
			if err != nil {
				errs <- err
				return
			}
			ids <- row.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent DeferWiring: %v", err)
	}
	first, ok := <-ids
	if !ok {
		t.Fatal("no concurrent DeferWiring returned a row")
	}
	for id := range ids {
		if id != first {
			t.Fatalf("concurrent DeferWiring returned ids %d and %d, want one row", first, id)
		}
	}
	if n := deferredCount(t, d); n != 1 {
		t.Fatalf("row count after concurrent defers = %d, want 1", n)
	}
}

// TestDeferWiringRejectsEmptyIdentity pins the boundary check: an empty watch
// id or reason cannot create a deferred row.
func TestDeferWiringRejectsEmptyIdentity(t *testing.T) {
	d := openDeferredTestDB(t)

	for _, in := range []DeferredWiring{
		{Reason: "reason"},
		{WatchID: "watch"},
		{WatchID: "   ", Reason: "reason"},
		{WatchID: "watch", Reason: "  "},
	} {
		if _, err := d.DeferWiring(in); err == nil {
			t.Errorf("DeferWiring(%+v) succeeded, want invalid input", in)
		}
	}
	if n := deferredCount(t, d); n != 0 {
		t.Fatalf("row count after invalid input = %d, want 0", n)
	}
}
