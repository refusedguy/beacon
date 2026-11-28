package scm

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
)

// glPageItem is the smallest provider shape the page walk needs to dedupe.
type glPageItem struct {
	ID int64 `json:"id"`
}

func glPageFixtureItems(from, count int) []glPageItem {
	items := make([]glPageItem, count)
	for i := range items {
		items[i].ID = int64(from + i)
	}
	return items
}

func glPageID(item glPageItem) int64 { return item.ID }

func assertGlPageItems(t *testing.T, got, want []glPageItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("items = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGetJSONPagesStopsOnFullLastPage(t *testing.T) {
	var requests atomic.Int64
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(gitlabPageSize) {
			t.Errorf("per_page = %q, want %d", got, gitlabPageSize)
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Errorf("page = %q, want 1", got)
		}
		w.Header().Set("X-Next-Page", "")
		glWriteJSON(t, w, glPageFixtureItems(1, gitlabPageSize))
	}))

	got, capped, err := getJSONPages[glPageItem](g, context.Background(), "/things", url.Values{}, glPageID)
	if err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if capped {
		t.Fatal("capped = true, want false")
	}
	assertGlPageItems(t, got, glPageFixtureItems(1, gitlabPageSize))
}

func TestGetJSONPagesFollowsAnnouncedNextPage(t *testing.T) {
	var requests atomic.Int64
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(gitlabPageSize) {
			t.Errorf("per_page = %q, want %d", got, gitlabPageSize)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			requests.Add(1)
			w.Header().Set("X-Next-Page", "2")
			glWriteJSON(t, w, glPageFixtureItems(1, gitlabPageSize))
		case "2":
			requests.Add(1)
			w.Header().Set("X-Next-Page", "")
			glWriteJSON(t, w, glPageFixtureItems(gitlabPageSize+1, gitlabPageSize))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			http.NotFound(w, r)
		}
	}))

	want := append(glPageFixtureItems(1, gitlabPageSize), glPageFixtureItems(gitlabPageSize+1, gitlabPageSize)...)
	got, capped, err := getJSONPages[glPageItem](g, context.Background(), "/things", url.Values{}, glPageID)
	if err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	if capped {
		t.Fatal("capped = true, want false")
	}
	assertGlPageItems(t, got, want)
}

func TestGetJSONPagesKeepsShortPageRuleWithoutHeader(t *testing.T) {
	var requests atomic.Int64
	g := glTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Query().Get("page") {
		case "1":
			glWriteJSON(t, w, glPageFixtureItems(1, gitlabPageSize))
		case "2":
			glWriteJSON(t, w, glPageFixtureItems(gitlabPageSize+1, gitlabPageSize/2))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			http.NotFound(w, r)
		}
	}))

	want := append(glPageFixtureItems(1, gitlabPageSize), glPageFixtureItems(gitlabPageSize+1, gitlabPageSize/2)...)
	got, capped, err := getJSONPages[glPageItem](g, context.Background(), "/things", url.Values{}, glPageID)
	if err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	if capped {
		t.Fatal("capped = true, want false")
	}
	assertGlPageItems(t, got, want)
}
