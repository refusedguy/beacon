package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// stubTier1Fetcher returns a fixed batch and records how many times it ran.
type stubTier1Fetcher struct {
	data  map[string]json.RawMessage
	calls int
}

func (f *stubTier1Fetcher) QueryTier1(_ context.Context, _ []domain.Watch) (*BatchResult, error) {
	f.calls++
	return &BatchResult{Data: f.data}, nil
}

// tier1PRJSON builds one aliased PR payload in the shape of tier1Fields.
func tier1PRJSON(rollup, mergeable, mergeState string) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"state":            "OPEN",
		"mergeable":        mergeable,
		"mergeStateStatus": mergeState,
		"commits": map[string]any{
			"nodes": []any{map[string]any{
				"commit": map[string]any{
					"statusCheckRollup": map[string]any{"state": rollup},
				},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// tier1Reply answers a recorded GraphQL query with one tier-1 PR per alias.
func tier1Reply(w http.ResponseWriter, query string) {
	data := make(map[string]any)
	for _, match := range queryAlias.FindAllStringSubmatch(query, -1) {
		data[match[1]] = map[string]any{
			"state":            "OPEN",
			"mergeable":        "MERGEABLE",
			"mergeStateStatus": "CLEAN",
			"commits": map[string]any{
				"nodes": []any{map[string]any{
					"commit": map[string]any{
						"statusCheckRollup": map[string]any{"state": "SUCCESS"},
					},
				}},
			},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"data":      data,
		"rateLimit": map[string]any{"cost": 1},
	})
}

func TestTier1IntervalBeforeAndAfterActiveWindow(t *testing.T) {
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		age  time.Duration
		want time.Duration
	}{
		{name: "just active", age: time.Second, want: Tier1FastInterval},
		{name: "just under five minutes", age: Tier1ActiveWindow - time.Second, want: Tier1FastInterval},
		{name: "exactly five minutes", age: Tier1ActiveWindow, want: Tier1SlowInterval},
		{name: "well past five minutes", age: 10 * time.Minute, want: Tier1SlowInterval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			last := base.Add(-tc.age)
			if got := Interval(base, last, false); got != tc.want {
				t.Fatalf("Interval = %v, want %v", got, tc.want)
			}
		})
	}
	if got := Interval(base, time.Time{}, false); got != Tier1SlowInterval {
		t.Fatalf("zero lastActivity: Interval = %v, want %v", got, Tier1SlowInterval)
	}
}

func TestPollerIntervalUsesFakeClock(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	p := NewPoller(nil, counter, nil)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	p.mu.Lock()
	p.lastActivity = now.Add(-4 * time.Minute)
	p.mu.Unlock()
	if got := p.Interval(); got != Tier1FastInterval {
		t.Fatalf("4 minutes of activity: Interval = %v, want %v", got, Tier1FastInterval)
	}

	p.mu.Lock()
	p.lastActivity = now.Add(-6 * time.Minute)
	p.mu.Unlock()
	if got := p.Interval(); got != Tier1SlowInterval {
		t.Fatalf("6 minutes of activity: Interval = %v, want %v", got, Tier1SlowInterval)
	}
}

func TestTier1IntervalStretchesAtLimit(t *testing.T) {
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	recent := base.Add(-time.Minute)
	old := base.Add(-10 * time.Minute)

	fast := Interval(base, recent, false)
	fastStretched := Interval(base, recent, true)
	if fastStretched != fast*Tier1StretchFactor {
		t.Fatalf("fast stretched = %v, want %v", fastStretched, fast*Tier1StretchFactor)
	}
	slow := Interval(base, old, false)
	slowStretched := Interval(base, old, true)
	if slowStretched != slow*Tier1StretchFactor {
		t.Fatalf("slow stretched = %v, want %v", slowStretched, slow*Tier1StretchFactor)
	}

	counter, _, _ := newTestCounter(t, 0)
	counter.Add(BudgetPerHour) // exhaust the T2.12 budget
	p := NewPoller(nil, counter, nil)
	p.now = func() time.Time { return base }
	p.mu.Lock()
	p.lastActivity = recent
	p.mu.Unlock()
	if !p.limitReached() {
		t.Fatal("limitReached = false at 500 points")
	}
	if got := p.Interval(); got != Tier1FastInterval*Tier1StretchFactor {
		t.Fatalf("poller interval at limit = %v, want %v", got, Tier1FastInterval*Tier1StretchFactor)
	}
}

func TestTier1ChunksAtMost100(t *testing.T) {
	chunks := Tier1Chunks(watches(250))
	var total int
	for i, chunk := range chunks {
		if len(chunk) == 0 || len(chunk) > ChunkSizeTier1 {
			t.Fatalf("chunk %d has %d aliases, want 1..%d", i, len(chunk), ChunkSizeTier1)
		}
		total += len(chunk)
	}
	if total != 250 {
		t.Fatalf("aliases in chunks = %d, want 250", total)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (100/100/50)", len(chunks))
	}
	seen := make(map[string]bool, total)
	for _, chunk := range chunks {
		for _, alias := range chunk {
			if alias.Name == "" || seen[alias.Name] {
				t.Fatalf("empty or duplicate alias name %q", alias.Name)
			}
			seen[alias.Name] = true
		}
	}
}

func TestTier1ParallelRequestsAtMostTwo(t *testing.T) {
	counter, _, _ := newTestCounter(t, 0)
	var active, maxActive atomic.Int64
	srv := newGraphQLServer(t, func(w http.ResponseWriter, r *http.Request, query string) {
		now := active.Add(1)
		for {
			seen := maxActive.Load()
			if now <= seen || maxActive.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		tier1Reply(w, query)
	})

	client := NewClient(srv.URL, "", srv.Client(), counter)
	p := NewPoller(client, counter, nil)
	if _, err := p.Tick(context.Background(), watches(500)); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := maxActive.Load(); got > MaxParallel || got < 2 {
		t.Fatalf("max parallel requests = %d, want between 2 and %d", got, MaxParallel)
	}
	if calls, sizes := srv.snapshot(); calls != 5 ||
		fmt.Sprint(sizes) != fmt.Sprint([]int{100, 100, 100, 100, 100}) {
		t.Fatalf("requests = %d, sizes = %v, want five chunks of 100", calls, sizes)
	}
}

func TestTier2CalledOnlyOnChangeOrUnknown(t *testing.T) {
	ws := watches(2)
	fetcher := &stubTier1Fetcher{data: map[string]json.RawMessage{}}
	var tier2Calls int
	var lastTier2 []domain.Watch
	p := NewPoller(fetcher, nil, func(_ context.Context, changed []domain.Watch) error {
		tier2Calls++
		lastTier2 = append([]domain.Watch(nil), changed...)
		return nil
	})
	ctx := context.Background()

	// First observation of a stable PR is not a change.
	fetcher.data["p0"] = tier1PRJSON("SUCCESS", "MERGEABLE", "CLEAN")
	fetcher.data["p1"] = tier1PRJSON("SUCCESS", "MERGEABLE", "CLEAN")
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 0 {
		t.Fatalf("tier-2 calls = %d after first observation, want 0", tier2Calls)
	}

	// Unchanged state still does not call tier-2.
	lastTier2 = nil
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 0 || lastTier2 != nil {
		t.Fatalf("tier-2 called for unchanged state: calls=%d watches=%v", tier2Calls, lastTier2)
	}

	// A rollup change escalates only the changed watch.
	lastTier2 = nil
	fetcher.data["p0"] = tier1PRJSON("PENDING", "MERGEABLE", "CLEAN")
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 1 || len(lastTier2) != 1 || lastTier2[0].ID != ws[0].ID {
		t.Fatalf("rollup change: calls=%d watches=%v, want 1 call for %s", tier2Calls, lastTier2, ws[0].ID)
	}

	// A merge-state change escalates only that watch.
	lastTier2 = nil
	fetcher.data["p1"] = tier1PRJSON("SUCCESS", "MERGEABLE", "DIRTY")
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 2 || len(lastTier2) != 1 || lastTier2[0].ID != ws[1].ID {
		t.Fatalf("merge-state change: calls=%d watches=%v, want 1 call for %s", tier2Calls, lastTier2, ws[1].ID)
	}

	// UNKNOWN escalates even with no change, and keeps escalating while it lasts.
	lastTier2 = nil
	fetcher.data["p0"] = tier1PRJSON("PENDING", "UNKNOWN", "CLEAN")
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 3 || len(lastTier2) != 1 || lastTier2[0].ID != ws[0].ID {
		t.Fatalf("UNKNOWN: calls=%d watches=%v, want 1 call for %s", tier2Calls, lastTier2, ws[0].ID)
	}
	lastTier2 = nil
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 4 || len(lastTier2) != 1 {
		t.Fatalf("persistent UNKNOWN: calls=%d watches=%v, want 1 call", tier2Calls, lastTier2)
	}

	// Resolving UNKNOWN without a rollup or merge-state change is not a trigger.
	lastTier2 = nil
	fetcher.data["p0"] = tier1PRJSON("PENDING", "MERGEABLE", "CLEAN")
	if _, err := p.Tick(ctx, ws); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if tier2Calls != 4 || lastTier2 != nil {
		t.Fatalf("resolved UNKNOWN triggered tier-2: calls=%d watches=%v", tier2Calls, lastTier2)
	}

	if fetcher.calls != 7 {
		t.Fatalf("tier-1 fetches = %d, want 7", fetcher.calls)
	}
}

func TestNeedsTier2unknownAndChanges(t *testing.T) {
	clean := Tier1Observation{Rollup: "SUCCESS", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}
	cases := []struct {
		name string
		prev Tier1Observation
		curr Tier1Observation
		want bool
	}{
		{name: "stable", prev: clean, curr: clean, want: false},
		{name: "first observation", curr: clean, want: false},
		{name: "rollup changed", prev: clean, curr: Tier1Observation{Rollup: "FAILURE", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}, want: true},
		{name: "merge state changed", prev: clean, curr: Tier1Observation{Rollup: "SUCCESS", Mergeable: "MERGEABLE", MergeStateStatus: "DIRTY"}, want: true},
		{name: "mergeable unknown", prev: clean, curr: Tier1Observation{Rollup: "SUCCESS", Mergeable: "UNKNOWN", MergeStateStatus: "CLEAN"}, want: true},
		{name: "merge state unknown", prev: clean, curr: Tier1Observation{Rollup: "SUCCESS", Mergeable: "MERGEABLE", MergeStateStatus: "UNKNOWN"}, want: true},
		{name: "rollup unknown", prev: clean, curr: Tier1Observation{Rollup: "UNKNOWN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsTier2(tc.prev, tc.curr); got != tc.want {
				t.Fatalf("NeedsTier2 = %v, want %v", got, tc.want)
			}
		})
	}
}
