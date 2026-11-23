// Package github holds the GitHub adapter for Beacon.
//
// This file implements the result cache from WORKER-PROMPT 4.6 and T2.15. A
// verdict is cached under a key made of the head SHA and a digest of the check
// suite composition and versions. A head SHA alone is not enough: a re-run
// keeps the SHA but resets suites to queued and clears conclusions. The cache
// therefore invalidates on a rerequest or suite-reset, downgrades the Watch to
// watching with a bumped revision, and never serves a cached ready without a
// fresh validation of the suite composition for the current SHA.
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// SuiteContext is one check context observed from the tier-2 rollup. Name and
// SuiteID capture the suite composition, Version captures a suite-level
// version (for example the check app version or the suite creation time) so a
// swap of suite versions changes the digest even when the head SHA is fixed.
// A future tier-2 integration feeds this from CheckRun.checkSuite.id and the
// mapped context names.
type SuiteContext struct {
	Name    string `json:"name"`
	SuiteID string `json:"suiteId"`
	Version string `json:"version,omitempty"`
}

// SuiteDigest returns a stable digest of a suite composition. The digest is
// independent of input order so the same set of contexts always maps to the
// same value. It changes when a context is added or removed, or when a suite
// id or version changes.
func SuiteDigest(contexts []SuiteContext) string {
	sorted := make([]SuiteContext, len(contexts))
	copy(sorted, contexts)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].SuiteID != sorted[j].SuiteID {
			return sorted[i].SuiteID < sorted[j].SuiteID
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Version < sorted[j].Version
	})
	// json.Marshal of a slice of string-only structs cannot fail.
	encoded, _ := json.Marshal(sorted)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Observation is one CI poll for a pull request.
type Observation struct {
	HeadSHA  string         // observed headRefOid
	Contexts []SuiteContext // current suite composition

	// Rerequest reports a rerequest action on a check suite. SuiteReset
	// reports that a suite was reset (queued, conclusion cleared). Either one
	// invalidates the cache and forces a re-verification.
	Rerequest  bool
	SuiteReset bool

	// SuitesValid reports that the suite composition was validated for the
	// observed SHA. A cached ready may only be served when this is true; this
	// is the cache-side counterpart of domain.Snapshot.SuitesValid.
	SuitesValid bool
}

// CacheResult is the decision returned by Cache.Resolve.
type CacheResult struct {
	Verdict   domain.Verdict // verdict the caller should apply
	FromCache bool           // true only for a validated cached ready
	Reverify  bool           // true when the caller must re-run the full checks
}

// maxCacheEntries bounds the verdict cache. A Watch cycle stores one entry
// per distinct (headSHA, suite digest); the bound keeps a long-lived process
// from holding verdicts for PRs that already closed. LEAK-OBS-01 will confirm
// the value against a heap profile under real concurrency.
const maxCacheEntries = 512

// Cache stores results by (headSHA, suite digest). It is safe for concurrent
// use. Entries are kept in insertion order so eviction removes the oldest
// SHA first, preserving verdicts for the Watches still in flight.
type Cache struct {
	mu      sync.Mutex
	entries map[cacheKey]domain.Verdict
	order   []cacheKey
}

type cacheKey struct {
	headSHA string
	digest  string
}

// NewCache builds an empty cache.
func NewCache() *Cache {
	return &Cache{
		entries: make(map[cacheKey]domain.Verdict),
		order:   make([]cacheKey, 0, maxCacheEntries),
	}
}

// Store records a computed verdict for an observation. The entry is keyed by
// the observation's head SHA and suite digest. When the cache is full the
// oldest entry is dropped; re-storing an existing key does not refresh its
// position, so one busy SHA cannot crowd out the others.
func (c *Cache) Store(obs Observation, verdict domain.Verdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[cacheKey]domain.Verdict)
	}
	key := keyFor(obs)
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = verdict
	for len(c.entries) > maxCacheEntries {
		c.forget(c.order[0])
	}
}

// Lookup returns a cached verdict for an observation without applying any
// validation gate. It is used by Resolve and by tests that check invalidation.
func (c *Cache) Lookup(obs Observation) (domain.Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[keyFor(obs)]
	return v, ok
}

// Invalidate removes every entry for headSHA, whatever its digest. A re-run
// keeps the SHA while resetting suites, so all digests for that SHA become
// suspect at once. Closing a Watch releases its SHA through InvalidateExcept,
// which shares this removal path.
func (c *Cache) Invalidate(headSHA string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.headSHA == headSHA {
			c.forget(k)
		}
	}
}

// InvalidateExcept removes every entry whose head SHA is absent from active.
// The batch adapter calls it with the SHAs observed in the current cycle, so a
// Watch that closed leaves no verdict behind before the bound is reached.
func (c *Cache) InvalidateExcept(active map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if _, live := active[k.headSHA]; !live {
			c.forget(k)
		}
	}
}

// forget removes one key from both the entries and the eviction order. The
// map may be mutated while ranging over it; order is kept in sync so the
// oldest live key is always at index zero.
func (c *Cache) forget(key cacheKey) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			copy(c.order[i:], c.order[i+1:])
			c.order[len(c.order)-1] = cacheKey{}
			c.order = c.order[:len(c.order)-1]
			return
		}
	}
}

// Resolve decides how to answer one observation for a Watch.
//
// On a rerequest or suite-reset it invalidates all entries for the SHA,
// downgrades w to watching with a bumped revision via the domain state machine,
// and asks for a re-verification. It never returns ready from this path.
//
// On a clean observation it serves a cached ready only when the suite
// composition was validated for this SHA. On a miss or any uncertainty it
// declines to issue a verdict and asks for a re-verification instead of
// trusting stale cache state.
func (c *Cache) Resolve(w domain.Watch, obs Observation, at time.Time) (CacheResult, domain.Watch) {
	if obs.Rerequest || obs.SuiteReset {
		c.Invalidate(obs.HeadSHA)
		next, _ := domain.Transition(w, domain.VerdictEvent(domain.VerdictWatching, at))
		return CacheResult{Verdict: domain.VerdictWatching, Reverify: true}, next
	}

	if cached, ok := c.Lookup(obs); ok && obs.SuitesValid && cached == domain.VerdictReady {
		return CacheResult{Verdict: domain.VerdictReady, FromCache: true}, w
	}

	// Uncertainty or miss: re-check before any ready can be issued. The watch
	// is not mutated here; only an explicit rerequest/reset bumps the revision.
	return CacheResult{Verdict: domain.VerdictWatching, Reverify: true}, w
}

func keyFor(obs Observation) cacheKey {
	return cacheKey{headSHA: obs.HeadSHA, digest: SuiteDigest(obs.Contexts)}
}
