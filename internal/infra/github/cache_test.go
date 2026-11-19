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
