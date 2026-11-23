package store

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func cardLinkCount(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	err := d.readDB.QueryRow(`SELECT count(*) FROM card_links`).Scan(&n)
	if err != nil {
		// The table is created lazily on the first accepted Link or Unlink.
		// A store that only saw rejected calls has no table yet, which means
		// it holds zero rows.
		if strings.Contains(err.Error(), "no such table: card_links") {
			return 0
		}
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
	d := openTestDB(t)

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
	d := openTestDB(t)

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
	d := openTestDB(t)

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
	d := openTestDB(t)

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
	d := openTestDB(t)

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
	d := openTestDB(t)

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
	d := openTestDB(t)

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

func TestLinkCardWatchStoresExplicitIDs(t *testing.T) {
	d := openTestDB(t)

	// The session id need not match the agent id written in the card. Only the
	// explicit cardID and watchID from the responses reach the store.
	res, err := d.LinkCardWatch("CARD-42", "ses_watch_77", 501)
	if err != nil {
		t.Fatalf("LinkCardWatch: %v", err)
	}
	if !res.Created || res.Duplicate {
		t.Fatalf("LinkCardWatch = %+v, want Created only", res)
	}
	want := CardLink{CardID: "CARD-42", WatchID: "ses_watch_77", PR: 501}
	if res.Link != want {
		t.Fatalf("LinkCardWatch row = %+v, want %+v", res.Link, want)
	}
	if got := storedCardLink(t, d, "CARD-42"); got != want {
		t.Fatalf("stored link = %+v, want %+v", got, want)
	}
}

func TestLinkCardWatchRejectsEmptyIDs(t *testing.T) {
	d := openTestDB(t)

	for _, tc := range []struct{ cardID, watchID string }{
		{"", "watch-9"},
		{"IDX-09", ""},
		{"", ""},
	} {
		if _, err := d.LinkCardWatch(tc.cardID, tc.watchID, 909); err == nil {
			t.Fatalf("LinkCardWatch(%q, %q) must fail", tc.cardID, tc.watchID)
		}
	}
	if rows := cardLinkCount(t, d); rows != 0 {
		t.Fatalf("rejected calls must not create rows, got %d", rows)
	}
}

func TestLinkCardWatchConcurrentSamePairCreatesOneRow(t *testing.T) {
	d := openTestDB(t)

	start := make(chan struct{})
	type callResult struct {
		res LinkResult
		err error
	}
	results := make(chan callResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			res, err := d.LinkCardWatch("IDX-09", "watch-9", 909)
			results <- callResult{res: res, err: err}
		}()
	}
	close(start)

	created := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent LinkCardWatch: %v", r.err)
		}
		if r.res.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("want exactly one Created result, got %d", created)
	}
	if rows := cardLinkCount(t, d); rows != 1 {
		t.Fatalf("concurrent LinkCardWatch: want 1 row, got %d", rows)
	}
	want := CardLink{CardID: "IDX-09", WatchID: "watch-9", PR: 909}
	if got := storedCardLink(t, d, "IDX-09"); got != want {
		t.Fatalf("stored link = %+v, want %+v", got, want)
	}
}
