package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// apiStatus is the status read GET /api/v1/runs needs. It mirrors the apiStatus
// interface in cmd/beacon/http.go; the benchmark lives in another package.
type apiStatus interface {
	Active(context.Context) ([]*core.Run, error)
}

// httpRunsCache mirrors runsCache.snapshot in cmd/beacon/http.go:57-75. The
// cache is invalidated before every timed pass so a pass measures the cache
// miss path, not the cached-byte return.
type httpRunsCache struct {
	mu   sync.Mutex
	body []byte
}

func (c *httpRunsCache) invalidate() {
	c.mu.Lock()
	c.body = nil
	c.mu.Unlock()
}

func (c *httpRunsCache) snapshot(ctx context.Context, status apiStatus) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body != nil {
		return c.body, nil
	}
	runs, err := status.Active(ctx)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(core.NewWireRuns(runs))
	if err != nil {
		return nil, err
	}
	c.body = append(b, '\n')
	return c.body, nil
}

// activeRunStore implements app.StatusStore with a fixed active-run set.
// ListActive returns fresh copies so no pass can reuse a decoded slice, and
// ListRecent is unused by the runs endpoint.
type activeRunStore struct {
	runs []*core.Run
}

func (s *activeRunStore) ListActive(context.Context) ([]*core.Run, error) {
	out := make([]*core.Run, len(s.runs))
	for i, run := range s.runs {
		copied := *run
		out[i] = &copied
	}
	return out, nil
}

func (s *activeRunStore) ListRecent(context.Context, int) ([]*core.Run, error) {
	return nil, nil
}

// activeRunFixture keeps only non-terminal runs from runFixtures, matching the
// active set status.Active returns in production. Every field stays populated
// so NewWireRuns and json.Marshal do real work.
func activeRunFixture(n int) []*core.Run {
	all := runFixtures(n * 3)
	out := make([]*core.Run, 0, n)
	for _, run := range all {
		if run.State.Terminal() {
			continue
		}
		out = append(out, run)
		if len(out) == n {
			break
		}
	}
	return out
}

// httpSnapshotBench drives the runs endpoint's cache-miss path through a real
// app.StatusService. runHTTPSnapshotAssembly resets the cache on every pass.
type httpSnapshotBench struct {
	ctx    context.Context
	status app.StatusService
	store  *activeRunStore
	cache  *httpRunsCache
	bytes  int
	body   []byte
}

func newHTTPSnapshotBench(tb testing.TB) *httpSnapshotBench {
	tb.Helper()
	store := &activeRunStore{runs: activeRunFixture(fixtureWireRuns)}
	return &httpSnapshotBench{
		ctx:    context.Background(),
		status: app.StatusService{Store: store},
		store:  store,
		cache:  &httpRunsCache{},
	}
}

// runHTTPSnapshotAssembly runs one cache-miss generation of GET /api/v1/runs.
// Work is the number of wire runs encoded, counted from the body so a change
// that drops runs cannot hide behind a flat timing number. Errors are failed
// snapshot calls. Body bytes accumulate for b.ReportMetric.
func runHTTPSnapshotAssembly(h *httpSnapshotBench) (int64, int64) {
	h.cache.invalidate()
	body, err := h.cache.snapshot(h.ctx, h.status)
	if err != nil {
		return 0, 1
	}
	h.body = body
	h.bytes += len(body)
	return int64(bytes.Count(body, []byte(`"task_id":`))), 0
}

// TestHTTPSnapshotAssemblyFixed proves the fixture is deterministic and that
// every pass runs the status read and wire encode instead of hitting the cache.
func TestHTTPSnapshotAssemblyFixed(t *testing.T) {
	h := newHTTPSnapshotBench(t)
	firstWork, firstErrs := runHTTPSnapshotAssembly(h)
	firstBody := append([]byte(nil), h.body...)
	secondWork, secondErrs := runHTTPSnapshotAssembly(h)
	if firstErrs != 0 || secondErrs != 0 {
		t.Fatalf("assembly errs = %d and %d", firstErrs, secondErrs)
	}
	if firstWork != secondWork || firstWork != fixtureWireRuns {
		t.Fatalf("work = %d then %d, want %d", firstWork, secondWork, fixtureWireRuns)
	}
	if !bytes.Equal(firstBody, h.body) {
		t.Fatal("encoded runs changed between identical passes")
	}
	var runs []core.WireRun
	if err := json.Unmarshal(firstBody, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != fixtureWireRuns {
		t.Fatalf("encoded %d runs, want %d", len(runs), fixtureWireRuns)
	}
	for _, run := range runs {
		if run.State == string(core.RunMerged) || run.State == string(core.RunClosed) {
			t.Fatalf("terminal run %s on the runs endpoint", run.ID)
		}
		if run.TaskID == "" || run.HeadSHA == "" || run.Branch == "" {
			t.Fatalf("run %s is missing wire fields: %+v", run.ID, run)
		}
	}
}

func TestHTTPSnapshotAssemblyFreshGeneration(t *testing.T) {
	h := newHTTPSnapshotBench(t)
	_, _ = runHTTPSnapshotAssembly(h)
	first := append([]byte(nil), h.body...)
	h.store.runs[0].State = core.RunBlocked
	_, _ = runHTTPSnapshotAssembly(h)
	if bytes.Equal(first, h.body) {
		t.Fatal("snapshot served the cached body after a run changed")
	}
}
