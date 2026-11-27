package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	refreshName          = "tui-refresh"
	refreshLimiter       = "debounce sleep + one GET per burst + generation ordering"
	refreshBurstEvents   = 200
	refreshRows          = 5
	refreshBenchDebounce = time.Millisecond
	refreshSmokeEnv      = "BEACON_BENCH_SMOKE"
)

// refreshCounts is one pass of the refresh harness. Every field is fixed by
// the fixture, so a smoke pass and a full pass print the same numbers.
type refreshCounts struct {
	work       int64 // accepted run snapshots
	errs       int64 // injected faults that the model handled
	gets       int64 // GETs that survived generation ordering
	stale      int64 // superseded replies dropped by Update
	burstGets  int64
	pingGets   int64
	reconnGets int64
	bad        int64 // harness checks that failed
}

// refreshCounter counts the runs reads served to the model.
type refreshCounter struct {
	mu   sync.Mutex
	gets int
}

func (c *refreshCounter) add() {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
}

func (c *refreshCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets
}

func (c *refreshCounter) reset() {
	c.mu.Lock()
	c.gets = 0
	c.mu.Unlock()
}

// refreshFixture holds the counting runs server and its client. The server
// serves the same five-run snapshot on every read.
type refreshFixture struct {
	runs    []Run
	server  *httptest.Server
	client  *Client
	counter *refreshCounter
}

func newRefreshFixture() *refreshFixture {
	runs := benchmarkFiveRuns("ready")
	counter := &refreshCounter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.add()
		if r.URL.Path != apiRunsPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Runs []Run `json:"runs"`
		}{Runs: runs})
	}))
	return &refreshFixture{
		runs:    runs,
		server:  server,
		client:  NewClient(server.URL, ""),
		counter: counter,
	}
}

func (f *refreshFixture) close() { f.server.Close() }
func (f *refreshFixture) reset() { f.counter.reset() }

// refreshFaultTransport returns the injected error without a network call, so
// the fault paths are deterministic and fast in every pass.
type refreshFaultTransport struct{ err error }

func (t refreshFaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func refreshFaultClient(err error) *Client {
	return &Client{
		Endpoint: "http://127.0.0.1:7432",
		HTTP:     &http.Client{Transport: refreshFaultTransport{err: err}, Timeout: requestTimeout},
	}
}

// refreshExec runs the refresh commands the way the Bubble Tea runtime does:
// concurrently, then collects their replies. Generation numbers were assigned
// before any command ran, so execution order cannot change which reply wins.
func refreshExec(cmds []tea.Cmd) []runsMsg {
	msgs := make([]runsMsg, 0, len(cmds))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		wg.Add(1)
		go func(cmd tea.Cmd) {
			defer wg.Done()
			msg := cmd().(runsMsg)
			mu.Lock()
			msgs = append(msgs, msg)
			mu.Unlock()
		}(cmd)
	}
	wg.Wait()
	return msgs
}

// runRefreshCoalesce drives one burst, a ping and a reconnect through the
// model, then checks the three error paths. The burst must cost one read, the
// ping none and the reconnect one. Every accepted snapshot must render exactly
// as viewBaseline does.
func runRefreshCoalesce(f *refreshFixture) refreshCounts {
	f.reset()
	var c refreshCounts

	m := newModel(f.client)
	m.debounce = refreshBenchDebounce
	m.width = 80

	cmds := make([]tea.Cmd, 0, refreshBurstEvents)
	for i := 0; i < refreshBurstEvents; i++ {
		next, cmd := m.onRefresh(Event{ID: int64(i + 1), Type: "run.updated"})
		m = next.(model)
		cmds = append(cmds, cmd)
	}
	msgs := refreshExec(cmds)
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].gen < msgs[j].gen })
	for _, msg := range msgs {
		if msg.stale || msg.gen != m.refreshGen.Load() {
			c.stale++
			continue
		}
		next, _ := m.Update(msg)
		m = next.(model)
		c.work++
	}
	c.burstGets = int64(f.counter.total())
	if got := m.View(); got != viewBaseline(m) {
		c.bad++
	}

	beforePing := f.counter.total()
	_, pingCmd := m.onRefresh(Event{Type: pingEvent})
	if pingCmd != nil {
		c.bad++
		_ = pingCmd()
	}
	c.pingGets = int64(f.counter.total() - beforePing)

	_, reconnectCmd := m.onRefresh(Event{Type: reconnectEventType})
	reconnectMsg := reconnectCmd().(runsMsg)
	if !reconnectMsg.stale && reconnectMsg.gen == m.refreshGen.Load() {
		next, _ := m.Update(reconnectMsg)
		m = next.(model)
		c.work++
	} else {
		c.bad++
	}
	c.reconnGets = int64(f.counter.total() - beforePing - int(c.pingGets))
	if got := m.View(); got != viewBaseline(m) {
		c.bad++
	}
	c.gets = c.burstGets + c.pingGets + c.reconnGets

	// Injected Runs error. The model must fail with the daemon's message.
	errClient := refreshFaultClient(errors.New("injected runs failure"))
	em := newModel(errClient)
	em.debounce = refreshBenchDebounce
	_, errCmd := em.onRefresh(Event{Type: "run.updated"})
	errMsg := errCmd().(runsMsg)
	if errMsg.err != nil {
		next, _ := em.Update(errMsg)
		em = next.(model)
		if em.failed && strings.Contains(em.status, "injected runs failure") {
			c.errs++
		} else {
			c.bad++
		}
	} else {
		c.bad++
	}

	// Context deadline. The model must fail instead of keeping a stale list.
	deadClient := refreshFaultClient(fmt.Errorf("injected deadline: %w", context.DeadlineExceeded))
	dm := newModel(deadClient)
	dm.debounce = refreshBenchDebounce
	_, deadCmd := dm.onRefresh(Event{Type: "run.updated"})
	deadMsg := deadCmd().(runsMsg)
	if errors.Is(deadMsg.err, context.DeadlineExceeded) {
		next, _ := dm.Update(deadMsg)
		dm = next.(model)
		if dm.failed {
			c.errs++
		} else {
			c.bad++
		}
	} else {
		c.bad++
	}

	// Snapshot mismatch. A stale reply carrying a different list must not
	// replace the snapshot the model accepted from the current generation.
	wrong := append(benchmarkFiveRuns("ready"), Run{
		ID: "run-extra", TaskID: "T-extra", State: "ready", PR: 99,
	})
	mismatch := runsMsg{runs: wrong, gen: 1, stale: true}
	before := append([]Run(nil), m.runs...)
	next, _ := m.Update(mismatch)
	m = next.(model)
	if reflect.DeepEqual(m.runs, before) {
		c.errs++
	} else {
		c.bad++
	}

	return c
}

// TestRefreshCoalesceCountsStable pins the reported counts. Two passes over
// the same fixture must agree, and the checks must catch all three faults.
func TestRefreshCoalesceCountsStable(t *testing.T) {
	f := newRefreshFixture()
	defer f.close()
	first := runRefreshCoalesce(f)
	second := runRefreshCoalesce(f)
	if first != second {
		t.Fatalf("refresh counts are not stable: first=%+v second=%+v", first, second)
	}
	if first.work != 2 || first.errs != 3 || first.gets != 2 ||
		first.burstGets != 1 || first.pingGets != 0 || first.reconnGets != 1 ||
		first.stale != refreshBurstEvents-1 || first.bad != 0 {
		t.Fatalf("refresh counts=%+v, want work=2 errs=3 gets=2 burst=1 ping=0 reconnect=1 stale=%d bad=0",
			first, refreshBurstEvents-1)
	}
}

func refreshSmokeMode() bool {
	v := os.Getenv(refreshSmokeEnv)
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err == nil && on
}

func refreshMedian(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), ds...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	mid := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[mid]
	}
	return ordered[mid-1] + (ordered[mid]-ordered[mid-1])/2
}

// printRefreshReport writes the summary so a benchmark run emits work, errs
// and the named limiter without -v.
func printRefreshReport(median time.Duration, c refreshCounts) {
	fmt.Printf("%s: median=%s work=%d errs=%d accepted-reads=%d burst-gets=%d ping-gets=%d reconnect-gets=%d superseded=%d limiter=%s\n",
		refreshName, median, c.work, c.errs, c.work, c.burstGets, c.pingGets,
		c.reconnGets, c.stale, refreshLimiter)
}

// BenchmarkRefreshCoalesce times event driven refresh coalescing and
// generation ordering in the monitor. One pass serves a 200-event burst, a
// ping and a reconnect, then exercises the Runs error, deadline and snapshot
// mismatch paths. Counts are fixed, so smoke and full passes agree.
//
// CI runs this benchmark with -count=5:
//
//	cd ui/tui && go test -run '^$' -bench '^BenchmarkRefreshCoalesce$' -benchmem -count=5 ./
//
// BEACON_BENCH_SMOKE=1 skips the warm-up pass. The fixture and every count are
// unchanged.
func BenchmarkRefreshCoalesce(b *testing.B) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	f := newRefreshFixture()
	defer f.close()
	if !refreshSmokeMode() {
		_ = runRefreshCoalesce(f)
	}

	var counts refreshCounts
	durations := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		counts = runRefreshCoalesce(f)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	if counts.bad != 0 {
		b.Fatalf("refresh checks failed: %+v", counts)
	}
	median := refreshMedian(durations)
	printRefreshReport(median, counts)
	b.ReportMetric(float64(median.Nanoseconds()), refreshName+"-median-ns")
	b.ReportMetric(float64(counts.work), refreshName+"-work")
	b.ReportMetric(float64(counts.errs), refreshName+"-errs")
	b.ReportMetric(float64(counts.gets), refreshName+"-limiter-coalesced-gets")
	b.ReportMetric(float64(refreshRows), "rows/op")
}
