package tasksource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

const (
	cardIndexName    = "card-index-invalidation"
	cardIndexLimiter = "filepath.WalkDir + parseCardFields over full corpus"

	smokeIndexCards  = 5000
	fullIndexCards   = 20000
	smokeIndexCycles = 5
	fullIndexCycles  = 10

	// The stability test uses a small corpus. It pins the counts CI compares
	// between base and head without writing thousands of files on every run.
	testIndexCards  = 100
	testIndexCycles = 5
)

// cardIndexSmokeMode reports whether CI set the bounded benchmark override.
func cardIndexSmokeMode() bool {
	on, err := strconv.ParseBool(os.Getenv("BEACON_BENCH_SMOKE"))
	return err == nil && on
}

type cardIndexFixture struct {
	dir    string
	store  CardStore
	cards  int
	cycles int
}

func newCardIndexFixture(tb testing.TB, cards, cycles int) cardIndexFixture {
	tb.Helper()
	dir := tb.TempDir()
	for i := 0; i < cards; i++ {
		writeIndexCard(tb, dir, "T-"+strconv.Itoa(i), "available")
	}
	return cardIndexFixture{dir: dir, store: CardStore{Dir: dir}, cards: cards, cycles: cycles}
}

func cycleID(k int) string { return "T-cycle-" + strconv.Itoa(k) }

// reset removes the cycle cards an earlier pass added and drops the cached
// index, so every pass starts from the same corpus with a zeroed scan count.
func (f cardIndexFixture) reset() {
	for k := 1; k <= f.cycles; k++ {
		_ = os.Remove(filepath.Join(f.dir, cycleID(k)+".md"))
	}
	resetCardIndex(f.dir)
	cardScanCount.Store(0)
}

type cardIndexCounts struct {
	work  int64
	errs  int64
	scans int64
}

// expectedCardIndexWork counts cards present at each rebuild plus the lookups
// served: one warm find and one find and patch per cycle.
func expectedCardIndexWork(cards, cycles int) int64 {
	cardsIndexed := int64(cards)
	for k := 1; k <= cycles; k++ {
		cardsIndexed += int64(cards + k)
	}
	return cardsIndexed + int64(1+2*cycles)
}

// runCardIndexInvalidation builds the index with one warm lookup, then runs
// cycles that add a card, move the directory mtime forward and look the new
// card up. Each cycle must trigger exactly one rebuild and no more. Work is
// fixed by the fixture, errs counts failed lookups and a wrong scan count.
func runCardIndexInvalidation(f cardIndexFixture) cardIndexCounts {
	f.reset()
	ctx := context.Background()
	var errs int64

	// The first lookup builds the index and takes the initial full scan.
	if _, err := f.store.indexedFind(core.TaskID("T-0")); err != nil {
		errs++
	}

	base := time.Now().Add(time.Hour)
	for k := 1; k <= f.cycles; k++ {
		id := core.TaskID(cycleID(k))
		content := "---\nid: " + string(id) +
			"\ntitle: Card " + string(id) +
			"\nstatus: available\n---\n# " + string(id) + ". Card\n"
		path := filepath.Join(f.dir, string(id)+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			errs++
			continue
		}
		// A future, increasing mtime makes dirMod differ at every cycle even
		// when the filesystem clock is coarse.
		stamp := base.Add(time.Duration(k) * time.Minute)
		if err := os.Chtimes(f.dir, stamp, stamp); err != nil {
			errs++
		}
		if _, err := f.store.indexedFind(id); err != nil {
			errs++
		}
		if err := f.store.indexedPatchStatus(ctx, id, "done"); err != nil {
			errs++
		}
	}

	scans := cardScanCount.Load()
	if scans != int64(1+f.cycles) {
		errs++
	}
	return cardIndexCounts{
		work:  expectedCardIndexWork(f.cards, f.cycles),
		errs:  errs,
		scans: scans,
	}
}

func medianCardIndexDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// TestCardIndexInvalidationWorkStable pins the counts CI compares between the
// base and head sides. Two passes over the same fixture report identical work,
// errs and scan counts, with no errors and one scan per rebuild.
func TestCardIndexInvalidationWorkStable(t *testing.T) {
	f := newCardIndexFixture(t, testIndexCards, testIndexCycles)
	first := runCardIndexInvalidation(f)
	again := runCardIndexInvalidation(f)
	if first != again {
		t.Fatalf("card index invalidation counts are not stable: first=%+v second=%+v", first, again)
	}
	if first.errs != 0 {
		t.Fatalf("card index invalidation errs=%d, want 0", first.errs)
	}
	if first.scans != int64(1+testIndexCycles) {
		t.Fatalf("card index invalidation scans=%d, want %d", first.scans, 1+testIndexCycles)
	}
	if first.work != expectedCardIndexWork(testIndexCards, testIndexCycles) {
		t.Fatalf("card index invalidation work=%d, want %d", first.work, expectedCardIndexWork(testIndexCards, testIndexCycles))
	}
}

// BenchmarkCardIndexInvalidation times the full index rebuild that follows a
// directory entry change. Fixture and reported counts are fixed, so a smoke
// pass and a full pass print the same work and errs.
//
// CI runs a bounded smoke pass with BEACON_BENCH_SMOKE=1 and -benchtime=1x:
//
//	BEACON_BENCH_SMOKE=1 GOMAXPROCS=1 go test -vet=off -run '^$' -bench '^BenchmarkCardIndexInvalidation$' -benchtime=1x -count=1 -benchmem ./internal/infra/tasksource/
func BenchmarkCardIndexInvalidation(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	cards, cycles := fullIndexCards, fullIndexCycles
	if cardIndexSmokeMode() {
		cards, cycles = smokeIndexCards, smokeIndexCycles
	}
	f := newCardIndexFixture(b, cards, cycles)
	if !cardIndexSmokeMode() {
		_ = runCardIndexInvalidation(f)
	}

	durations := make([]time.Duration, 0, b.N)
	var counts cardIndexCounts
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		counts = runCardIndexInvalidation(f)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	median := medianCardIndexDuration(durations)
	fmt.Printf("%s: median=%s work=%d errs=%d scans=%d limiter=%s\n",
		cardIndexName, median, counts.work, counts.errs, counts.scans, cardIndexLimiter)
	b.ReportMetric(float64(median.Nanoseconds()), cardIndexName+"-median-ns")
	b.ReportMetric(float64(counts.work), cardIndexName+"-work")
	b.ReportMetric(float64(counts.errs), cardIndexName+"-errs")
	b.ReportMetric(float64(counts.scans), cardIndexName+"-scans")
}
