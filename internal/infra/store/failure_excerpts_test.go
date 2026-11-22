package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func openFailureExcerptCache(t *testing.T) *FailureExcerptCache {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "excerpts.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewFailureExcerptCache(d)
}

// A cache miss is not an error. It tells the caller to fetch from GitHub.
func TestFailureExcerptCacheMiss(t *testing.T) {
	c := openFailureExcerptCache(t)
	got, found, err := c.Get(context.Background(), "watch-1", "sha-1", "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatalf("Get found = true on a missing key")
	}
	if got != "" {
		t.Errorf("Get excerpt = %q, want empty", got)
	}
}

func TestFailureExcerptCachePutGet(t *testing.T) {
	c := openFailureExcerptCache(t)
	ctx := context.Background()
	if err := c.Put(ctx, "watch-1", "sha-1", "test", "the failure text"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := c.Get(ctx, "watch-1", "sha-1", "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatalf("Get found = false after Put")
	}
	if got != "the failure text" {
		t.Errorf("Get excerpt = %q, want %q", got, "the failure text")
	}
}

// The first answer wins. A later Put on the same key must not overwrite it.
func TestFailureExcerptCacheKeepsFirstOnConflict(t *testing.T) {
	c := openFailureExcerptCache(t)
	ctx := context.Background()
	if err := c.Put(ctx, "watch-1", "sha-1", "test", "first"); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := c.Put(ctx, "watch-1", "sha-1", "test", "second"); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	got, found, err := c.Get(ctx, "watch-1", "sha-1", "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || got != "first" {
		t.Errorf("Get = (%q, %v), want (%q, true)", got, found, "first")
	}
}

func TestFailureExcerptCacheHeadSHAInKey(t *testing.T) {
	c := openFailureExcerptCache(t)
	ctx := context.Background()
	if err := c.Put(ctx, "watch-1", "sha-1", "test", "old result"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := c.Get(ctx, "watch-1", "sha-2", "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatalf("Get found = true for a new head SHA")
	}
	if got != "" {
		t.Errorf("Get excerpt = %q, want empty", got)
	}
}

// An empty result is cached too, so a check without annotations is not
// fetched again on the next poll.
func TestFailureExcerptCacheCachesEmpty(t *testing.T) {
	c := openFailureExcerptCache(t)
	ctx := context.Background()
	if err := c.Put(ctx, "watch-1", "sha-1", "test", ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := c.Get(ctx, "watch-1", "sha-1", "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatalf("Get found = false for a cached empty excerpt")
	}
	if got != "" {
		t.Errorf("Get excerpt = %q, want empty", got)
	}
}

func TestBoundExcerpt(t *testing.T) {
	t.Run("at the limit", func(t *testing.T) {
		in := strings.Repeat("a", FailureExcerptLimit)
		if got := boundExcerpt(in); got != in {
			t.Errorf("boundExcerpt changed a string at the limit: len = %d", len(got))
		}
	})

	t.Run("over the limit", func(t *testing.T) {
		in := strings.Repeat("a", FailureExcerptLimit+10)
		got := boundExcerpt(in)
		if len(got) != FailureExcerptLimit {
			t.Errorf("boundExcerpt len = %d, want %d", len(got), FailureExcerptLimit)
		}
	})

	t.Run("cuts at a rune boundary", func(t *testing.T) {
		// The euro sign is three bytes. 682 of them fill 2046 bytes, so a
		// raw cut at 2048 would leave a partial rune at the end.
		in := strings.Repeat("\u20ac", 700)
		got := boundExcerpt(in)
		if !utf8.ValidString(got) {
			t.Fatalf("boundExcerpt returned invalid UTF-8")
		}
		want := strings.Repeat("\u20ac", 682)
		if got != want {
			t.Errorf("boundExcerpt len = %d, want 2046 (682 runes)", len(got))
		}
	})
}

func TestFailureExcerptCachePutBoundsExcerpt(t *testing.T) {
	c := openFailureExcerptCache(t)
	ctx := context.Background()
	in := strings.Repeat("a", FailureExcerptLimit+50)
	if err := c.Put(ctx, "watch-1", "sha-1", "test", in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := c.Get(ctx, "watch-1", "sha-1", "test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || len(got) != FailureExcerptLimit {
		t.Errorf("cached excerpt len = %d, found = %v; want %d, true", len(got), found, FailureExcerptLimit)
	}
}

// A disabled cache is a no-op, not a panic.
func TestFailureExcerptCacheNilReceiver(t *testing.T) {
	ctx := context.Background()
	var c *FailureExcerptCache

	got, found, err := c.Get(ctx, "watch-1", "sha-1", "test")
	if err != nil || found || got != "" {
		t.Errorf("nil Get = (%q, %v, %v), want (\"\", false, nil)", got, found, err)
	}
	if err := c.Put(ctx, "watch-1", "sha-1", "test", "text"); err != nil {
		t.Errorf("nil Put: %v", err)
	}
}
