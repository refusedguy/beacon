// Package orchestrator coordinates maintenance of pull requests that still
// hold an active Watch after another PR has been merged. This file adds the
// dependency-safe merge order and the chain state used after a rebase
// conflict. The order function is pure: the caller supplies the graph edges
// and this package never reads the store or sends a Wake.
package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// MergeCandidate is one ready Watch paired with the dependency edges the
// caller already computed from the task graph. DependsOn holds the IDs of
// Watches that must reach main before this one can merge. The caller owns the
// resolver; this package only reads the values it was handed.
type MergeCandidate struct {
	Watch     Watch
	DependsOn []string
}

// mergeCandidateID is the identity used by DependsOn. A Watch ID is stable;
// the PR number is the fallback for callers that resolve edges by PR.
func mergeCandidateID(c MergeCandidate) string {
	if id := strings.TrimSpace(c.Watch.ID); id != "" {
		return id
	}
	return fmt.Sprintf("pr/%d", c.Watch.PR)
}

// SafeMergeOrder orders ready Watches so a dependency always comes before the
// Watch that waits for it. A dependency missing from candidates has not
// reached main yet, so its dependents land in deferred and stay out of the
// merge result. Independent Watches are sorted by PR and then ID, which keeps
// the result stable between ticks. A dependency cycle is deferred as well:
// nothing in it can be merged safely.
func SafeMergeOrder(candidates []MergeCandidate) (ordered, deferred []MergeCandidate) {
	byID := make(map[string]MergeCandidate, len(candidates))
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		id := mergeCandidateID(c)
		if _, exists := byID[id]; exists {
			deferred = append(deferred, c)
			continue
		}
		byID[id] = c
		ids = append(ids, id)
	}

	// A missing dependency, or a dependency that is itself blocked, blocks a
	// candidate. Iterate to a fixed point so a whole chain stays out until
	// its root lands in main.
	blocked := make(map[string]bool, len(ids))
	for changed := true; changed; {
		changed = false
		for _, id := range ids {
			if blocked[id] {
				continue
			}
			for _, dep := range byID[id].DependsOn {
				dep = strings.TrimSpace(dep)
				if dep == "" || dep == id {
					continue
				}
				if _, ok := byID[dep]; !ok || blocked[dep] {
					blocked[id] = true
					changed = true
					break
				}
			}
		}
	}

	indegree := make(map[string]int, len(ids))
	dependents := make(map[string][]string, len(ids))
	for _, id := range ids {
		if blocked[id] {
			continue
		}
		seen := make(map[string]bool)
		for _, dep := range byID[id].DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" || dep == id || seen[dep] {
				continue
			}
			seen[dep] = true
			indegree[id]++
			dependents[dep] = append(dependents[dep], id)
		}
	}

	ready := make([]string, 0, len(ids))
	for _, id := range ids {
		if !blocked[id] && indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sortReady := func() {
		sort.Slice(ready, func(i, j int) bool {
			a, b := byID[ready[i]], byID[ready[j]]
			if a.Watch.PR != b.Watch.PR {
				return a.Watch.PR < b.Watch.PR
			}
			return mergeCandidateID(a) < mergeCandidateID(b)
		})
	}
	sortReady()
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, next := range dependents[id] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
		sortReady()
	}

	deferIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if blocked[id] || indegree[id] > 0 {
			deferIDs = append(deferIDs, id)
		}
	}
	sort.Strings(deferIDs)
	for _, id := range deferIDs {
		deferred = append(deferred, byID[id])
	}
	return ordered, deferred
}

// DeferredChain is the unmerged remainder of a safe order. It lives in memory
// in the composition root: pausing and resuming write no store state and send
// no Wake, so the rebase contract in rebase.go stays unchanged.
type DeferredChain struct {
	pending  []MergeCandidate
	pausedOn string
}

// NewDeferredChain starts a chain from the ordered result of SafeMergeOrder.
func NewDeferredChain(ordered []MergeCandidate) *DeferredChain {
	return &DeferredChain{pending: append([]MergeCandidate(nil), ordered...)}
}

// PauseOnConflict parks the chain on the Watch whose rebase conflict was
// reported. That Watch stays at the head: its head SHA changed, so the caller
// re-checks it after the executor resolves the conflict.
func (c *DeferredChain) PauseOnConflict(watchID string) {
	if c == nil {
		return
	}
	c.pausedOn = strings.TrimSpace(watchID)
}

// PausedOn reports the Watch the chain waits on. An empty value means the
// chain can continue.
func (c *DeferredChain) PausedOn() string {
	if c == nil {
		return ""
	}
	return c.pausedOn
}

// ConflictResolved consumes the signal that the executor resolved the
// conflict for watchID and returns the remaining candidates in merge order.
// A signal for another Watch leaves the chain paused and returns an error, so
// an unrelated conflict cannot resume this chain.
func (c *DeferredChain) ConflictResolved(watchID string) ([]MergeCandidate, error) {
	if c == nil {
		return nil, fmt.Errorf("orchestrator: nil deferred merge chain")
	}
	if c.pausedOn == "" {
		return nil, fmt.Errorf("orchestrator: merge chain is not paused")
	}
	if strings.TrimSpace(watchID) != c.pausedOn {
		return nil, fmt.Errorf("orchestrator: conflict resolved for %q, chain paused on %q", watchID, c.pausedOn)
	}
	c.pausedOn = ""
	return append([]MergeCandidate(nil), c.pending...), nil
}

// Advance drops a Watch from the head after its merge succeeded. A Watch that
// is not at the head is left in place; the chain never reorders itself behind
// the caller.
func (c *DeferredChain) Advance(watchID string) {
	if c == nil || len(c.pending) == 0 {
		return
	}
	if mergeCandidateID(c.pending[0]) == strings.TrimSpace(watchID) {
		c.pending = c.pending[1:]
	}
}

// MergeQueuePort is the wiring point for a follow-up PR in cibridge. The
// adapter supplies ready Watches with edges already computed from the graph
// and reports which Watch had its conflict resolved. SafeMergeOrder and
// DeferredChain take plain data, so neither the store nor the Wake protocol
// changes here.
type MergeQueuePort interface {
	ReadyCandidates(ctx context.Context) ([]MergeCandidate, error)
	ConflictResolved(ctx context.Context) (string, error)
}

// NextMerge is the tick-level call a cibridge adapter will make. It resumes a
// paused chain when the conflict signal names its Watch; otherwise it builds
// a new safe order from the supplied candidates. Deferred Watches come back
// to the caller so they stay parked until their dependency reaches main.
func NextMerge(ctx context.Context, port MergeQueuePort, chain *DeferredChain) (ordered, deferred []MergeCandidate, err error) {
	if port == nil {
		return nil, nil, fmt.Errorf("orchestrator: merge queue port is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if chain != nil && chain.PausedOn() != "" {
		id, signalErr := port.ConflictResolved(ctx)
		if signalErr != nil {
			return nil, nil, fmt.Errorf("orchestrator: conflict signal: %w", signalErr)
		}
		resumed, resumeErr := chain.ConflictResolved(id)
		if resumeErr != nil {
			return nil, nil, resumeErr
		}
		return resumed, nil, nil
	}
	candidates, sourceErr := port.ReadyCandidates(ctx)
	if sourceErr != nil {
		return nil, nil, fmt.Errorf("orchestrator: ready merge candidates: %w", sourceErr)
	}
	ordered, deferred = SafeMergeOrder(candidates)
	return ordered, deferred, nil
}
