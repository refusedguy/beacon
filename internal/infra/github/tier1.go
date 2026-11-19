// Package github holds the GitHub adapter for Beacon.
//
// This file implements the tier-1 poller from T2.13. It batches the state
// fields of active watches, runs on an adaptive 10s/30s interval, stretches the
// interval when the shared point budget from T2.12 is exhausted, and escalates
// a watch to tier-2 only on a rollup or merge-state change or an UNKNOWN value.
// The tier-2 callback is injected so this file compiles before T2.14 lands and
// is wired to the real tier-2 fetcher when it does.
package github

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// Tier1FastInterval is used while the newest activity is younger than the
	// active window (WORKER-PROMPT 4.6, T2.13).
	Tier1FastInterval = 10 * time.Second
	// Tier1SlowInterval is used once the newest activity is older than the
	// active window.
	Tier1SlowInterval = 30 * time.Second
	// Tier1ActiveWindow is the age at which the poller slows down.
	Tier1ActiveWindow = 5 * time.Minute
	// Tier1StretchFactor multiplies the interval while the budget limit flag
	// from T2.12 is set. A 10s tick becomes 30s and a 30s tick becomes 90s.
	Tier1StretchFactor = 3
)

// Interval selects the tier-1 poll interval. A zero or old lastActivity runs
// at the slow interval; a lastActivity strictly inside the active window runs
// at the fast interval. The limit flag stretches either value.
func Interval(now, lastActivity time.Time, limitReached bool) time.Duration {
	base := Tier1SlowInterval
	if !lastActivity.IsZero() && now.Sub(lastActivity) < Tier1ActiveWindow {
		base = Tier1FastInterval
	}
	if limitReached {
		base *= Tier1StretchFactor
	}
	return base
}

// Tier1Observation is the slice of a PR snapshot the poller remembers between
// ticks to detect state changes.
type Tier1Observation struct {
	Rollup           string
	MergeStateStatus string
	Mergeable        string
	HeadSHA          string
}

// NeedsTier2 reports whether the transition from prev to curr needs the
// tier-2 contexts fetch. Tier-2 runs on a rollup or merge-state change or when
// a tracked field reads UNKNOWN. A first observation with an empty prev and no
// UNKNOWN is not a change, so it does not by itself trigger tier-2.
func NeedsTier2(prev, curr Tier1Observation) bool {
	if isUnknown(curr.Rollup) || isUnknown(curr.Mergeable) || isUnknown(curr.MergeStateStatus) {
		return true
	}
	if prev == (Tier1Observation{}) {
		return false
	}
	return !strings.EqualFold(prev.Rollup, curr.Rollup) ||
		!strings.EqualFold(prev.MergeStateStatus, curr.MergeStateStatus)
}

func isUnknown(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "UNKNOWN")
}

// tier1PR mirrors the fields rendered by tier1Fields in graphql.go.
type tier1PR struct {
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	HeadRefOid       string `json:"headRefOid"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	Commits          struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

func observationFrom(raw json.RawMessage) Tier1Observation {
	var pr tier1PR
	if err := json.Unmarshal(raw, &pr); err != nil {
		return Tier1Observation{}
	}
	var rollup string
	if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		rollup = pr.Commits.Nodes[0].Commit.StatusCheckRollup.State
	}
	return Tier1Observation{
		Rollup:           rollup,
		MergeStateStatus: pr.MergeStateStatus,
		Mergeable:        pr.Mergeable,
		HeadSHA:          pr.HeadRefOid,
	}
}

// Tier1Chunks builds the tier-1 batch for the active watches and splits it
// into chunks of at most ChunkSizeTier1 (100) aliases. Closed watches are
// skipped and duplicate PRs collapse by the same rule as AliasesFromWatches.
func Tier1Chunks(watches []domain.Watch) [][]Alias {
	return Chunk(AliasesFromWatches(watches), Tier1)
}

// Tier1Fetcher fetches one tier-1 batch. *Client implements it; a stub may be
// injected in tests.
type Tier1Fetcher interface {
	QueryTier1(ctx context.Context, watches []domain.Watch) (*BatchResult, error)
}

// Tier2Func refreshes tier-2 contexts for watches that changed. Production
// wiring passes the T2.14 tier-2 fetcher, optionally through the T2.15 cache.
// Tier-1 depends only on this seam, so it builds before T2.14 is merged.
type Tier2Func func(ctx context.Context, watches []domain.Watch) error

// Poller polls tier-1 state for active watches on an adaptive interval.
type Poller struct {
	fetcher    Tier1Fetcher
	counter    *Counter
	fetchTier2 Tier2Func
	now        func() time.Time
	sleep      func(time.Duration)

	mu           sync.Mutex
	prev         map[string]Tier1Observation
	lastActivity time.Time
	seenSHA      map[string]bool // head SHAs already escalated to tier-2 (ADR 0033 §1)
}

// NewPoller builds a tier-1 poller. counter supplies the shared budget flag
// from T2.12 and may be nil. tier2 is called only for changed watches and may
// be nil. Replace now and sleep in tests to use a fake clock.
func NewPoller(fetcher Tier1Fetcher, counter *Counter, tier2 Tier2Func) *Poller {
	return &Poller{
		fetcher:    fetcher,
		counter:    counter,
		fetchTier2: tier2,
		now:        time.Now,
		sleep:      time.Sleep,
		prev:       make(map[string]Tier1Observation),
	}
}

// Interval returns the interval the poller would wait after the current tick.
func (p *Poller) Interval() time.Duration {
	p.mu.Lock()
	last := p.lastActivity
	p.mu.Unlock()
	return Interval(p.now(), last, p.limitReached())
}

func (p *Poller) limitReached() bool {
	return p.counter != nil && p.counter.LimitReached()
}

// Tick runs one tier-1 poll and returns the interval to wait before the next.
// The batch is fetched through Tier1Fetcher, so the chunk cap of 100 aliases
// and the cap of two parallel requests come from the GraphQL client in T2.11.
func (p *Poller) Tick(ctx context.Context, watches []domain.Watch) (time.Duration, error) {
	if p.fetcher == nil {
		return p.Interval(), nil
	}
	result, err := p.fetcher.QueryTier1(ctx, watches)
	if err != nil {
		return p.Interval(), err
	}
	if result == nil {
		return p.Interval(), nil
	}

	byName := watchByAlias(watches)
	changed := make([]domain.Watch, 0)
	current := make(map[string]Tier1Observation, len(byName))
	active := false
	for _, alias := range AliasesFromWatches(watches) {
		watch, ok := byName[alias.Name]
		if !ok {
			continue
		}
		curr := observationFrom(result.Data[alias.Name])
		current[watch.ID] = curr
		prev := p.prev[watch.ID]
		if NeedsTier2(prev, curr) {
			changed = append(changed, watch)
			active = true
		}
	}

	p.mu.Lock()
	p.prev = current
	if active {
		p.lastActivity = p.now()
	}
	changed = p.dedupBySHA(current, changed)
	p.mu.Unlock()

	if len(changed) > 0 && p.fetchTier2 != nil {
		if err := p.fetchTier2(ctx, changed); err != nil {
			return p.Interval(), err
		}
	}
	return p.Interval(), nil
}

// dedupBySHA keeps at most one watch per head SHA for the tier-2 escalation.
// A head commit is immutable, so once one PR carrying a SHA has been fetched
// from tier-2, a second PR with the same SHA in this batch (or a later one)
// reuses that answer instead of issuing a redundant query (ADR 0033 §1).
// Watches without a head SHA are left untouched because they cannot be
// deduplicated. The caller must hold p.mu.
func (p *Poller) dedupBySHA(current map[string]Tier1Observation, changed []domain.Watch) []domain.Watch {
	if p.seenSHA == nil {
		p.seenSHA = make(map[string]bool)
	}
	out := make([]domain.Watch, 0, len(changed))
	for _, w := range changed {
		sha := current[w.ID].HeadSHA
		if sha != "" {
			if p.seenSHA[sha] {
				continue
			}
			p.seenSHA[sha] = true
		}
		out = append(out, w)
	}
	return out
}

// Run polls watches from the supplier until ctx is done, sleeping the
// adaptive interval between ticks. The first tick runs immediately.
func (p *Poller) Run(ctx context.Context, watches func() []domain.Watch) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := p.Tick(ctx, watches())
		if err != nil {
			return err
		}
		p.sleep(next)
	}
}

// watchByAlias maps the unique alias names built by AliasesFromWatches back to
// their watches. Matching on owner, repo and PR avoids repeating the naming
// rule; the first watch wins for duplicate PRs, as in AliasesFromWatches.
func watchByAlias(watches []domain.Watch) map[string]domain.Watch {
	byKey := make(map[string]domain.Watch, len(watches))
	for _, w := range watches {
		if w.Status == domain.StatusClosed {
			continue
		}
		owner, repo := splitRepo(w.Repo)
		key := owner + "/" + repo + "#" + strconv.Itoa(w.PR)
		if _, exists := byKey[key]; !exists {
			byKey[key] = w
		}
	}
	out := make(map[string]domain.Watch, len(byKey))
	for _, alias := range AliasesFromWatches(watches) {
		key := alias.Owner + "/" + alias.Repo + "#" + strconv.Itoa(alias.PR)
		if w, ok := byKey[key]; ok {
			out[alias.Name] = w
		}
	}
	return out
}
