package github

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

func suiteContexts() []SuiteContext {
	return []SuiteContext{
		{Name: "build", SuiteID: "S1", Version: "v1"},
		{Name: "test", SuiteID: "S1", Version: "v1"},
		{Name: "lint", SuiteID: "S2", Version: "v2"},
	}
}

func readyWatch() domain.Watch {
	return domain.Watch{
		ID:       "w1",
		Repo:     "refusedguy/beacon",
		PR:       7,
		HeadSHA:  "sha-abc",
		Status:   domain.StatusNotifying,
		Verdict:  domain.VerdictReady,
		Revision: 5,
	}
}

func observationFor(contexts []SuiteContext) Observation {
	return Observation{
		HeadSHA:     "sha-abc",
		Contexts:    contexts,
		SuitesValid: true,
	}
}

func TestSuiteDigestStableAndSensitive(t *testing.T) {
	contexts := suiteContexts()
	base := SuiteDigest(contexts)

	reordered := []SuiteContext{contexts[2], contexts[0], contexts[1]}
	if got := SuiteDigest(reordered); got != base {
		t.Fatal("digest depends on input order, want order independent")
	}

	cases := []struct {
		name    string
		mutate  func() []SuiteContext
		wantNew bool
	}{
		{
			name: "suite id change",
			mutate: func() []SuiteContext {
				out := suiteContexts()
				out[0].SuiteID = "S9"
				return out
			},
			wantNew: true,
		},
		{
			name: "version change",
			mutate: func() []SuiteContext {
				out := suiteContexts()
				out[2].Version = "v3"
				return out
			},
			wantNew: true,
		},
		{
			name: "context removed",
			mutate: func() []SuiteContext {
				return suiteContexts()[:2]
			},
			wantNew: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SuiteDigest(tc.mutate()); got == base {
				t.Fatalf("digest did not change after %s", tc.name)
			}
		})
	}
}

func TestCachedReadyNeedsValidation(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	c.Store(obs, domain.VerdictReady)

	validated, _ := c.Resolve(readyWatch(), obs, time.Now())
	if !validated.FromCache || validated.Verdict != domain.VerdictReady || validated.Reverify {
		t.Fatalf("validated hit = %+v, want cached ready with no reverify", validated)
	}

	unvalidated := obs
	unvalidated.SuitesValid = false
	got, _ := c.Resolve(readyWatch(), unvalidated, time.Now())
	if got.FromCache || got.Verdict == domain.VerdictReady || !got.Reverify {
		t.Fatalf("unvalidated hit = %+v, want no ready and a reverify", got)
	}
}

func TestRerunInvalidatesAndBumpsRevision(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	c.Store(obs, domain.VerdictReady)

	if v, ok := c.Lookup(obs); !ok || v != domain.VerdictReady {
		t.Fatalf("lookup before rerun = %q, %t, want ready, true", v, ok)
	}

	// Re-run: same SHA, suite reset to queued with the composition unchanged.
	rerun := obs
	rerun.SuiteReset = true
	rerun.Contexts = []SuiteContext{
		{Name: "build", SuiteID: "S1", Version: "v1"},
		{Name: "test", SuiteID: "S1", Version: "v1"},
		{Name: "lint", SuiteID: "S2", Version: "v2"},
	}
	got, next := c.Resolve(readyWatch(), rerun, time.Now())

	if got.Verdict != domain.VerdictWatching || !got.Reverify || got.FromCache {
		t.Fatalf("rerun result = %+v, want watching with reverify and no cache hit", got)
	}
	if next.Verdict != domain.VerdictWatching {
		t.Fatalf("watch verdict = %q, want watching", next.Verdict)
	}
	if next.Revision != 6 {
		t.Fatalf("watch revision = %d, want 6 (5 + 1)", next.Revision)
	}
	if v, ok := c.Lookup(obs); ok {
		t.Fatalf("cache still holds %q after rerun, want invalidated", v)
	}
}

func TestRerequestActionAlsoInvalidates(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	c.Store(obs, domain.VerdictReady)

	requested := obs
	requested.Rerequest = true
	got, _ := c.Resolve(readyWatch(), requested, time.Now())
	if !got.Reverify || got.Verdict == domain.VerdictReady {
		t.Fatalf("rerequest result = %+v, want reverify and no ready", got)
	}
	if _, ok := c.Lookup(obs); ok {
		t.Fatal("rerequest did not invalidate the cache")
	}
}

func TestCacheKeyIncludesHeadSHA(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	c.Store(obs, domain.VerdictReady)

	other := obs
	other.HeadSHA = "sha-different"
	if v, ok := c.Lookup(other); ok {
		t.Fatalf("same suite composition on another SHA hit cache entry %q", v)
	}
	got, _ := c.Resolve(readyWatch(), other, time.Now())
	if got.FromCache || !got.Reverify {
		t.Fatalf("other SHA result = %+v, want miss with reverify", got)
	}
}

func TestInvalidateKeepsOtherSHAs(t *testing.T) {
	c := NewCache()
	first := observationFor(suiteContexts())
	second := observationFor(suiteContexts())
	second.HeadSHA = "sha-keep"
	c.Store(first, domain.VerdictReady)
	c.Store(second, domain.VerdictReady)

	c.Invalidate(first.HeadSHA)
	if _, ok := c.Lookup(first); ok {
		t.Fatal("first SHA was not invalidated")
	}
	if v, ok := c.Lookup(second); !ok || v != domain.VerdictReady {
		t.Fatalf("second SHA = %q, %t, want untouched ready entry", v, ok)
	}
}

func TestUncertaintyForcesReverification(t *testing.T) {
	c := NewCache()
	w := readyWatch()
	w.Verdict = domain.VerdictWatching
	w.Status = domain.StatusWatching

	got, next := c.Resolve(w, observationFor(suiteContexts()), time.Now())
	if !got.Reverify || got.FromCache || got.Verdict == domain.VerdictReady {
		t.Fatalf("miss result = %+v, want reverify and no ready", got)
	}
	if next.Revision != w.Revision || next.Verdict != w.Verdict {
		t.Fatalf("plain miss mutated the watch: %+v", next)
	}
}

func TestCacheConcurrentUse(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := obs
			o.HeadSHA = fmt.Sprintf("sha-%d", i%4)
			c.Store(o, domain.VerdictReady)
			c.Lookup(o)
			c.Resolve(readyWatch(), o, time.Now())
			if i%2 == 0 {
				c.Invalidate(o.HeadSHA)
			}
		}(i)
	}
	wg.Wait()
}

func TestCacheCapacityEvictsOldestSHA(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	for i := 0; i < maxCacheEntries+2; i++ {
		o := obs
		o.HeadSHA = fmt.Sprintf("sha-%04d", i)
		c.Store(o, domain.VerdictReady)
	}

	if len(c.entries) != maxCacheEntries {
		t.Fatalf("entries = %d, want %d", len(c.entries), maxCacheEntries)
	}
	if len(c.order) != maxCacheEntries {
		t.Fatalf("order = %d, want %d", len(c.order), maxCacheEntries)
	}
	for i := 0; i < 2; i++ {
		o := obs
		o.HeadSHA = fmt.Sprintf("sha-%04d", i)
		if _, ok := c.Lookup(o); ok {
			t.Fatalf("oldest SHA %d is still cached", i)
		}
	}
	for i := 2; i < maxCacheEntries+2; i++ {
		o := obs
		o.HeadSHA = fmt.Sprintf("sha-%04d", i)
		if v, ok := c.Lookup(o); !ok || v != domain.VerdictReady {
			t.Fatalf("SHA %d = %q, %t, want ready, true", i, v, ok)
		}
	}
}

func TestStoreExistingKeyDoesNotRefreshPosition(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	for i := 0; i < maxCacheEntries; i++ {
		o := obs
		o.HeadSHA = fmt.Sprintf("sha-%04d", i)
		c.Store(o, domain.VerdictReady)
	}

	first := obs
	first.HeadSHA = "sha-0000"
	c.Store(first, domain.VerdictReady)
	if len(c.order) != maxCacheEntries {
		t.Fatalf("order after re-store = %d, want %d", len(c.order), maxCacheEntries)
	}

	next := obs
	next.HeadSHA = "sha-next"
	c.Store(next, domain.VerdictReady)
	if len(c.entries) != maxCacheEntries {
		t.Fatalf("entries = %d, want %d", len(c.entries), maxCacheEntries)
	}
	if _, ok := c.Lookup(first); ok {
		t.Fatal("re-stored oldest SHA was kept ahead of newer entries")
	}
	if v, ok := c.Lookup(next); !ok || v != domain.VerdictReady {
		t.Fatalf("newest SHA = %q, %t, want ready, true", v, ok)
	}
}

func TestInvalidateKeepsEvictionOrderInSync(t *testing.T) {
	c := NewCache()
	obs := observationFor(suiteContexts())
	for i := 0; i < maxCacheEntries; i++ {
		o := obs
		o.HeadSHA = fmt.Sprintf("sha-%04d", i)
		c.Store(o, domain.VerdictReady)
	}

	c.Invalidate("sha-0001")
	if len(c.order) != maxCacheEntries-1 {
		t.Fatalf("order after invalidate = %d, want %d", len(c.order), maxCacheEntries-1)
	}

	next := obs
	next.HeadSHA = "sha-next"
	c.Store(next, domain.VerdictReady)
	if len(c.entries) != maxCacheEntries || len(c.order) != maxCacheEntries {
		t.Fatalf("sizes = %d entries, %d order; want %d each", len(c.entries), len(c.order), maxCacheEntries)
	}
	oldest := obs
	oldest.HeadSHA = "sha-0000"
	if _, ok := c.Lookup(oldest); ok {
		t.Fatal("oldest live SHA was not evicted after invalidation")
	}
	if v, ok := c.Lookup(next); !ok || v != domain.VerdictReady {
		t.Fatalf("newest SHA = %q, %t, want ready, true", v, ok)
	}
	for _, key := range c.order {
		if _, ok := c.entries[key]; !ok {
			t.Fatalf("order holds ghost key %+v", key)
		}
	}
}

func TestInvalidateExceptReleasesClosedWatchSHA(t *testing.T) {
	c := NewCache()
	live := observationFor(suiteContexts())
	closed := observationFor(suiteContexts())
	closed.HeadSHA = "sha-closed"
	c.Store(live, domain.VerdictReady)
	c.Store(closed, domain.VerdictReady)

	c.InvalidateExcept(map[string]struct{}{live.HeadSHA: {}})
	if v, ok := c.Lookup(closed); ok {
		t.Fatalf("closed Watch left verdict %q in the cache", v)
	}
	if v, ok := c.Lookup(live); !ok || v != domain.VerdictReady {
		t.Fatalf("live SHA = %q, %t, want ready, true", v, ok)
	}
	if len(c.order) != 1 || c.order[0].headSHA != live.HeadSHA {
		t.Fatalf("order = %+v, want only the live SHA", c.order)
	}
}
