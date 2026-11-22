package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// fakeCIFailedLogSource returns a fixed log and counts calls. The count is the
// evidence that paging uses the cache instead of fetching the log again.
type fakeCIFailedLogSource struct {
	mu    sync.Mutex
	text  string
	calls int
}

func (f *fakeCIFailedLogSource) FailureLog(_ context.Context, _ string, _ string, _ string, _ int, _ int) (string, int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.text, len(f.text), len(f.text), nil
}

func (f *fakeCIFailedLogSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// memoryCIFailedLogCache is a map-backed failureExcerptCache.
type memoryCIFailedLogCache struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemoryCIFailedLogCache() *memoryCIFailedLogCache {
	return &memoryCIFailedLogCache{m: make(map[string]string)}
}

func (c *memoryCIFailedLogCache) key(watchID, headSHA, checkName string) string {
	return watchID + "\x00" + headSHA + "\x00" + checkName
}

func (c *memoryCIFailedLogCache) Get(_ context.Context, watchID, headSHA, checkName string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[c.key(watchID, headSHA, checkName)]
	return v, ok, nil
}

func (c *memoryCIFailedLogCache) Put(_ context.Context, watchID, headSHA, checkName, excerpt string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(watchID, headSHA, checkName)] = excerpt
	return nil
}

// ciLogTestStore is the minimal read port the handler needs.
type ciLogTestStore struct {
	watch domain.Watch
}

func (s ciLogTestStore) Watch(id string) (domain.Watch, error) {
	if id != s.watch.ID {
		return domain.Watch{}, domain.NewAppError(domain.CodeWatchNotFound, "no such watch")
	}
	return s.watch, nil
}

func (s ciLogTestStore) Obligation(string) (domain.Obligation, error) {
	return domain.Obligation{}, nil
}

func ciLogTestWatch(conclusion string) domain.Watch {
	return domain.Watch{
		ID:      "w-1",
		Repo:    "owner/repo",
		HeadSHA: "deadbeef",
		Checks: map[string]domain.Check{
			"ci/test": {
				Conclusion: conclusion,
				State:      "COMPLETED",
				DetailsURL: "https://github.com/owner/repo/runs/1",
				Required:   true,
			},
		},
	}
}

func TestCIFailedLogPaginatesFromCache(t *testing.T) {
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	raw := strings.Repeat("a", 4000) + token + strings.Repeat("b", 5000)

	src := &fakeCIFailedLogSource{text: raw}
	cache := newMemoryCIFailedLogCache()
	h := NewCIFailedLogHandler(ciLogTestStore{watch: ciLogTestWatch("FAILURE")}, src)
	h.SetCache(cache)

	first, out, err := h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/test", LimitBytes: 2048,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first == nil {
		t.Fatal("first page: nil tool result")
	}
	if len(out.Text) != 2048 {
		t.Fatalf("first page length = %d, want 2048", len(out.Text))
	}
	if out.NextOffset != 2048 {
		t.Fatalf("first nextOffset = %d, want 2048", out.NextOffset)
	}
	if out.FromCache {
		t.Fatal("first page must come from the source")
	}
	if out.TotalBytes != 4000+len("[REDACTED]")+5000 {
		t.Fatalf("totalBytes = %d, want sanitized length", out.TotalBytes)
	}
	if out.LogURL != "https://github.com/owner/repo/runs/1" {
		t.Fatalf("logUrl = %q", out.LogURL)
	}

	second, out2, err := h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/test", Offset: out.NextOffset, LimitBytes: 2048,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second == nil {
		t.Fatal("second page: nil tool result")
	}
	if len(out2.Text) != 2048 {
		t.Fatalf("second page length = %d, want 2048", len(out2.Text))
	}
	if out2.NextOffset != 4096 {
		t.Fatalf("second nextOffset = %d, want 4096", out2.NextOffset)
	}
	if !out2.FromCache {
		t.Fatal("second page must come from the cache")
	}
	if got := src.callCount(); got != 1 {
		t.Fatalf("source calls = %d, want 1", got)
	}
}

func TestCIFailedLogRedactsToken(t *testing.T) {
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	raw := strings.Repeat("a", 4000) + token + strings.Repeat("b", 5000)

	h := NewCIFailedLogHandler(ciLogTestStore{watch: ciLogTestWatch("FAILURE")}, &fakeCIFailedLogSource{text: raw})
	h.SetCache(newMemoryCIFailedLogCache())

	_, out, err := h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/test", Offset: 3800, LimitBytes: 2048,
	})
	if err != nil {
		t.Fatalf("token page: %v", err)
	}
	if strings.Contains(out.Text, "ghp_") {
		t.Fatal("raw GitHub token reached the output")
	}
	if !strings.Contains(out.Text, "[REDACTED]") {
		t.Fatal("redaction marker is missing from the output")
	}
}

func TestCIFailedLogClampsLimit(t *testing.T) {
	raw := strings.Repeat("x", 100000)
	h := NewCIFailedLogHandler(ciLogTestStore{watch: ciLogTestWatch("ERROR")}, &fakeCIFailedLogSource{text: raw})
	h.SetCache(newMemoryCIFailedLogCache())

	_, out, err := h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/test", LimitBytes: 999999,
	})
	if err != nil {
		t.Fatalf("clamped page: %v", err)
	}
	if len(out.Text) != ciFailedLogMaxLimit {
		t.Fatalf("clamped page length = %d, want %d", len(out.Text), ciFailedLogMaxLimit)
	}
	if !out.Truncated {
		t.Fatal("a page shorter than the whole log must be marked truncated")
	}
	if out.NextOffset != ciFailedLogMaxLimit {
		t.Fatalf("clamped nextOffset = %d, want %d", out.NextOffset, ciFailedLogMaxLimit)
	}
}

func TestCIFailedLogRefusesUnknownCheck(t *testing.T) {
	h := NewCIFailedLogHandler(ciLogTestStore{watch: ciLogTestWatch("FAILURE")}, &fakeCIFailedLogSource{text: "log"})

	_, _, err := h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/passing",
	})
	if err == nil {
		t.Fatal("unknown checkName must be refused")
	}

	_, _, err = h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/test", Offset: -1,
	})
	if err == nil {
		t.Fatal("negative offset must be refused")
	}
}

func TestCIFailedLogRefusesPassingCheck(t *testing.T) {
	h := NewCIFailedLogHandler(ciLogTestStore{watch: ciLogTestWatch("SUCCESS")}, &fakeCIFailedLogSource{text: "log"})

	_, _, err := h.Handle(context.Background(), nil, CIFailedLogInput{
		WatchID: "w-1", CheckName: "ci/test",
	})
	if err == nil {
		t.Fatal("a check that is not failed must be refused")
	}
}
