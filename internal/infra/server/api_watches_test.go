package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

type stubWatchReader struct {
	watches []domain.Watch
	err     error
	calls   int
}

func (s *stubWatchReader) Watches(context.Context) ([]domain.Watch, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.watches, nil
}

func sampleWatches() []domain.Watch {
	return []domain.Watch{
		{ID: "w1", Repo: "refusedguy/beacon", PR: 1, Status: domain.StatusWatching,
			Verdict: domain.VerdictWatching, Revision: 3,
			Subscribers: []domain.Subscriber{
				{ID: "ses_exec", Role: domain.RoleExecutor},
				{ID: "ses_orch", Role: domain.RoleOrchestrator},
			}},
		{ID: "w2", Repo: "refusedguy/beacon", PR: 2, Status: domain.StatusNotifying,
			Verdict: domain.VerdictReady, Revision: 4,
			Subscribers: []domain.Subscriber{{ID: "ses_fix", Role: domain.RoleFixer}}},
		{ID: "w3", Repo: "other/repo", PR: 9, Status: domain.StatusBlocked,
			Verdict: domain.VerdictBlocked, Revision: 7},
	}
}

func TestWatchesPageListsVerdictRevisionSubscribers(t *testing.T) {
	input := sampleWatches()
	reader := &stubWatchReader{watches: input}

	page, err := NewWatchProjection(reader).Page(context.Background(), WatchQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Watches, input) {
		t.Fatalf("page watches = %+v, want the reader's watches unchanged", page.Watches)
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want 1", reader.calls)
	}
	first := page.Watches[0]
	if first.Verdict != domain.VerdictWatching || first.Revision != 3 {
		t.Fatalf("first watch verdict/revision = %q/%d, want watching/3", first.Verdict, first.Revision)
	}
	if len(first.Subscribers) != 2 || first.Subscribers[0].ID != "ses_exec" {
		t.Fatalf("first watch subscribers = %+v, want ses_exec and ses_orch", first.Subscribers)
	}
}

func TestWatchesCountsMatchListRunning(t *testing.T) {
	// beacon_list_running counts every filtered watch, grouped by verdict, and
	// sums the subscribers. The projection must report the same totals.
	page, err := NewWatchProjection(&stubWatchReader{watches: sampleWatches()}).
		Page(context.Background(), WatchQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := WatchCounts{
		Watches:     3,
		ByVerdict:   map[string]int{"watching": 1, "ready": 1, "blocked": 1},
		Subscribers: 3,
	}
	if !reflect.DeepEqual(page.Counts, want) {
		t.Fatalf("counts = %+v, want %+v", page.Counts, want)
	}
	if len(page.Watches) != 2 {
		t.Fatalf("page holds %d watches, want the limit of 2", len(page.Watches))
	}
}

func TestWatchesPageFilters(t *testing.T) {
	cases := []struct {
		name    string
		verdict string
		status  string
		wantIDs []string
	}{
		{name: "verdict", verdict: "ready", wantIDs: []string{"w2"}},
		{name: "status", status: "blocked", wantIDs: []string{"w3"}},
		{name: "both", verdict: "watching", status: "watching", wantIDs: []string{"w1"}},
		{name: "no match", verdict: "merged", wantIDs: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := NewWatchProjection(&stubWatchReader{watches: sampleWatches()}).
				Page(context.Background(), WatchQuery{Verdict: tc.verdict, Status: tc.status, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(page.Watches))
			for _, w := range page.Watches {
				got = append(got, w.ID)
			}
			if !reflect.DeepEqual(got, tc.wantIDs) {
				t.Fatalf("watches = %v, want %v", got, tc.wantIDs)
			}
			if page.Counts.Watches != len(tc.wantIDs) {
				t.Fatalf("counts.watches = %d, want %d", page.Counts.Watches, len(tc.wantIDs))
			}
		})
	}
}

func TestWatchesPageEmpty(t *testing.T) {
	page, err := NewWatchProjection(&stubWatchReader{}).
		Page(context.Background(), WatchQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Watches == nil || len(page.Watches) != 0 {
		t.Fatalf("empty watches = %+v, want an empty non-nil slice", page.Watches)
	}
	if page.Counts.ByVerdict == nil || len(page.Counts.ByVerdict) != 0 || page.Counts.Subscribers != 0 {
		t.Fatalf("empty counts = %+v, want zero counts and an empty map", page.Counts)
	}
	if page.NextCursor != "" {
		t.Fatalf("empty next cursor = %q, want empty", page.NextCursor)
	}
}

func TestWatchesReaderError(t *testing.T) {
	sentinel := errors.New("store down")
	reader := &stubWatchReader{err: sentinel}
	projection := NewWatchProjection(reader)

	if _, err := projection.Page(context.Background(), WatchQuery{}); !errors.Is(err, sentinel) {
		t.Fatalf("Page error = %v, want wrapped %v", err, sentinel)
	}
	rr := httptest.NewRecorder()
	projection.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, WatchesPath, nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var body watchesErrorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "WATCHES_FAILED" || body.OK {
		t.Fatalf("error body = %+v, want WATCHES_FAILED", body)
	}
}

func TestWatchesPagination(t *testing.T) {
	watches := make([]domain.Watch, 5)
	for i := range watches {
		watches[i] = domain.Watch{ID: "w" + strconv.Itoa(i+1), Status: domain.StatusWatching,
			Verdict: domain.VerdictWatching}
	}
	projection := NewWatchProjection(&stubWatchReader{watches: watches})
	q := WatchQuery{Limit: 2}
	seen := make([]string, 0, len(watches))
	pages := 0
	for {
		page, err := projection.Page(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Counts.Watches != len(watches) {
			t.Fatalf("counts.watches = %d on page %d, want the full set %d",
				page.Counts.Watches, pages, len(watches))
		}
		for _, w := range page.Watches {
			seen = append(seen, w.ID)
		}
		if page.NextCursor == "" {
			break
		}
		q.Cursor = page.NextCursor
		if pages > len(watches) {
			t.Fatal("pagination did not terminate")
		}
	}
	want := []string{"w1", "w2", "w3", "w4", "w5"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("paged ids = %v, want %v", seen, want)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3 for five watches at limit 2", pages)
	}
}

func TestWatchesHandlerFiltersAndEmptyState(t *testing.T) {
	rr := httptest.NewRecorder()
	NewWatchProjection(&stubWatchReader{watches: sampleWatches()}).Handler().
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, WatchesPath+"?verdict=ready", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var page WatchesPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Watches) != 1 || page.Watches[0].ID != "w2" {
		t.Fatalf("filtered watches = %+v, want w2", page.Watches)
	}

	empty := httptest.NewRecorder()
	NewWatchProjection(&stubWatchReader{}).Handler().
		ServeHTTP(empty, httptest.NewRequest(http.MethodGet, WatchesPath, nil))
	var emptyPage WatchesPage
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyPage); err != nil {
		t.Fatal(err)
	}
	if emptyPage.Watches == nil || len(emptyPage.Watches) != 0 {
		t.Fatalf("empty response watches = %+v, want []", emptyPage.Watches)
	}
}

func TestWatchesHandlerReadOnly(t *testing.T) {
	rr := httptest.NewRecorder()
	NewWatchProjection(&stubWatchReader{}).Handler().
		ServeHTTP(rr, httptest.NewRequest(http.MethodPost, WatchesPath, nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestWatchesHandlerRejectsInvalidQuery(t *testing.T) {
	for _, path := range []string{
		WatchesPath + "?cursor=not-base64!",
		WatchesPath + "?cursor=LTEx", // base64 of "-11"
		WatchesPath + "?limit=0",
		WatchesPath + "?limit=abc",
	} {
		rr := httptest.NewRecorder()
		NewWatchProjection(&stubWatchReader{watches: sampleWatches()}).Handler().
			ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, rr.Code)
		}
	}
}

func TestWatchesNilReaderIsAnError(t *testing.T) {
	var projection *WatchProjection
	if _, err := projection.Page(context.Background(), WatchQuery{}); err == nil {
		t.Fatal("nil projection Page returned no error")
	}
	if _, err := NewWatchProjection(nil).Page(context.Background(), WatchQuery{}); err == nil {
		t.Fatal("nil reader Page returned no error")
	}
}
