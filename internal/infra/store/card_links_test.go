package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func newCardLinkTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func cardLinkCount(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.readDB.QueryRow(`SELECT count(*) FROM card_links`).Scan(&n); err != nil {
		t.Fatalf("count card links: %v", err)
	}
	return n
}

func storedCardLink(t *testing.T, d *DB, cardID string) CardLink {
	t.Helper()
	link, err := loadCardLink(d.readDB, cardID)
	if err != nil {
		t.Fatalf("load card link %q: %v", cardID, err)
	}
	return link
}

func TestLinkCreatesOneRowAndRepeatsAreNoops(t *testing.T) {
	d := newCardLinkTestDB(t)

	first, err := d.Link("IDX-07", "watch-1", 123)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if !first.Created || first.Duplicate {
		t.Fatalf("first Link = %+v, want Created only", first)
	}
	if first.Link != (CardLink{CardID: "IDX-07", WatchID: "watch-1", PR: 123}) {
		t.Fatalf("first Link row = %+v", first.Link)
	}

	repeat, err := d.Link("IDX-07", "watch-1", 123)
	if err != nil {
		t.Fatalf("repeat Link: %v", err)
	}
	if !repeat.Duplicate || repeat.Created {
		t.Fatalf("repeat Link = %+v, want Duplicate only", repeat)
	}
	if n := cardLinkCount(t, d); n != 1 {
		t.Fatalf("after repeat: want 1 row, got %d", n)
	}

	other, err := d.Link("IDX-07", "watch-2", 124)
	if err != nil {
		t.Fatalf("Link with another watch: %v", err)
	}
	if !other.Duplicate || other.Created {
		t.Fatalf("Link with another watch = %+v, want Duplicate only", other)
	}
	if got := storedCardLink(t, d, "IDX-07"); got != first.Link {
		t.Fatalf("stored link changed to %+v, want %+v", got, first.Link)
	}
	if n := cardLinkCount(t, d); n != 1 {
		t.Fatalf("after conflicting repeat: want 1 row, got %d", n)
	}
}

func TestUnlinkIsIdempotent(t *testing.T) {
	d := newCardLinkTestDB(t)

	if _, err := d.Link("IDX-07", "watch-1", 123); err != nil {
		t.Fatalf("Link: %v", err)
	}
	removed, err := d.Unlink("IDX-07")
	if err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if !removed {
		t.Fatal("first Unlink must report a removed row")
	}
	if n := cardLinkCount(t, d); n != 0 {
		t.Fatalf("after Unlink: want 0 rows, got %d", n)
	}

	removed, err = d.Unlink("IDX-07")
	if err != nil {
		t.Fatalf("repeat Unlink: %v", err)
	}
	if removed {
		t.Fatal("repeat Unlink must report no removed row")
	}
	if n := cardLinkCount(t, d); n != 0 {
		t.Fatalf("after repeat Unlink: want 0 rows, got %d", n)
	}
}

func TestUnlinkOnFreshStoreDoesNothing(t *testing.T) {
	d := newCardLinkTestDB(t)

	removed, err := d.Unlink("IDX-07")
	if err != nil {
		t.Fatalf("Unlink on a fresh store: %v", err)
	}
	if removed {
		t.Fatal("Unlink of a missing card must report no removed row")
	}
	if n := cardLinkCount(t, d); n != 0 {
		t.Fatalf("fresh store: want 0 rows, got %d", n)
	}
}

func TestLinkRejectsEmptyIDs(t *testing.T) {
	d := newCardLinkTestDB(t)

	for _, tc := range []struct{ cardID, watchID string }{
		{"", "watch-1"},
		{"IDX-07", ""},
	} {
		if _, err := d.Link(tc.cardID, tc.watchID, 123); err == nil {
			t.Fatalf("Link(%q, %q) must fail", tc.cardID, tc.watchID)
		}
	}
	if _, err := d.Unlink(""); err == nil {
		t.Fatal("Unlink with an empty card id must fail")
	}
}

func TestLinkConcurrentSameCardCreatesOneRow(t *testing.T) {
	d := newCardLinkTestDB(t)

	const callers = 16
	var wg sync.WaitGroup
	created := make(chan bool, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := d.Link("IDX-07", "watch-1", 123)
			if err != nil {
				errs <- err
				return
			}
			created <- res.Created
		}()
	}
	wg.Wait()
	close(created)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Link: %v", err)
	}
	n := 0
	for ok := range created {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one Created result, got %d", n)
	}
	if rows := cardLinkCount(t, d); rows != 1 {
		t.Fatalf("concurrent Link: want 1 row, got %d", rows)
	}
	if got := storedCardLink(t, d, "IDX-07"); got != (CardLink{CardID: "IDX-07", WatchID: "watch-1", PR: 123}) {
		t.Fatalf("stored link = %+v", got)
	}
}

func TestLinkConcurrentDistinctCardsCreatesEveryRow(t *testing.T) {
	d := newCardLinkTestDB(t)

	const callers = 16
	var wg sync.WaitGroup
	created := make(chan bool, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := d.Link(fmt.Sprintf("IDX-%02d", i), "watch-1", 100+i)
			if err != nil {
				errs <- err
				return
			}
			created <- res.Created
		}(i)
	}
	wg.Wait()
	close(created)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Link of distinct cards: %v", err)
	}
	n := 0
	for ok := range created {
		if ok {
			n++
		}
	}
	if n != callers {
		t.Fatalf("want %d Created results, got %d", callers, n)
	}
	if rows := cardLinkCount(t, d); rows != callers {
		t.Fatalf("concurrent Link: want %d rows, got %d", callers, rows)
	}
}

func TestLinkAndUnlinkConcurrentKeepAtMostOneRow(t *testing.T) {
	d := newCardLinkTestDB(t)

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = d.Link("IDX-07", "watch-1", 123)
			} else {
				_, err = d.Unlink("IDX-07")
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Link and Unlink: %v", err)
	}
	if rows := cardLinkCount(t, d); rows > 1 {
		t.Fatalf("concurrent Link and Unlink: want at most 1 row, got %d", rows)
	}
}
