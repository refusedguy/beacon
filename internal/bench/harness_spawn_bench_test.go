package bench

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/pkg/core"
)

// spawnCount bounds the smoke work. Ten spawns cover the create, model write,
// prompt and deferred move without a slow fixture.
const spawnCount = 10

// spawnHarnessBench holds one OpenCode harness on an in-process transport.
// Counters are read before and after each batch so every row reports the
// requests it served. The move channel carries one token per deferred move,
// so the caller can count them after the benchmark timer stops.
type spawnHarnessBench struct {
	harness    *opencode.Harness
	ctx        context.Context
	creates    *atomic.Int64
	models     *atomic.Int64
	moves      *atomic.Int64
	prompts    *atomic.Int64
	unexpected *atomic.Int64
	moved      chan struct{}
}

func newSpawnHarnessBench(b *testing.B) *spawnHarnessBench {
	b.Helper()
	return newSpawnHarness(b, b.N)
}

// newSpawnHarness builds the fixture for a caller with an explicit iteration
// count. Tests pass one batch; benchmarks pass b.N.
func newSpawnHarness(tb testing.TB, iterations int) *spawnHarnessBench {
	tb.Helper()
	var creates, models, moves, prompts, unexpected, next atomic.Int64
	// One warm batch plus one batch per timed iteration. The channel is large
	// enough that a deferred move never blocks the transport.
	moved := make(chan struct{}, (iterations+1)*spawnCount)
	transport := stubRoundTripper(func(req *http.Request) (*http.Response, error) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && p == "/api/session":
			creates.Add(1)
			id := fmt.Sprintf("ses_%d", next.Add(1))
			return stubJSON(req, http.StatusOK, fmt.Sprintf(`{"id":%q}`, id)), nil
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/model"):
			models.Add(1)
			return stubJSON(req, http.StatusOK, `{}`), nil
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/move"):
			moves.Add(1)
			moved <- struct{}{}
			return stubJSON(req, http.StatusOK, `{}`), nil
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/prompt"):
			prompts.Add(1)
			return stubJSON(req, http.StatusOK, `{"delivery":"steer"}`), nil
		default:
			unexpected.Add(1)
			return stubJSON(req, http.StatusNotFound, `{"message":"not found"}`), nil
		}
	})
	return &spawnHarnessBench{
		harness: opencode.New(opencode.Config{
			Endpoint: "http://bench.invalid",
			Model:    "bench-model",
			Client:   &http.Client{Transport: transport},
		}),
		ctx:        context.Background(),
		creates:    &creates,
		models:     &models,
		moves:      &moves,
		prompts:    &prompts,
		unexpected: &unexpected,
		moved:      moved,
	}
}

func spawnBenchRequest(i int) core.SpawnRequest {
	return core.SpawnRequest{
		Task:      core.Task{ID: core.TaskID(fmt.Sprintf("T-%04d", i)), Title: "bench spawn"},
		Workspace: core.Workspace{Path: "/tmp/beacon-bench-ws/" + fmt.Sprint(i), Branch: "beacon/bench"},
		RunID:     core.RunID(fmt.Sprintf("run-%04d", i)),
		Prompt:    "implement the task",
	}
}

// runHarnessSpawn completes ten Spawn calls. Work is spawns plus the loopback
// requests they serve, including the deferred move; errors are Spawn failures
// and requests the fixture does not model. The deferred moves are awaited before
// the counters are read, so the published work is the final count rather than a
// snapshot taken while move goroutines are still in flight.
func runHarnessSpawn(h *spawnHarnessBench) (int64, int64) {
	var work, errs int64
	beforeCreates := h.creates.Load()
	beforeModels := h.models.Load()
	beforeMoves := h.moves.Load()
	beforePrompts := h.prompts.Load()
	beforeUnexpected := h.unexpected.Load()
	for i := 0; i < spawnCount; i++ {
		if _, err := h.harness.Spawn(h.ctx, spawnBenchRequest(i)); err != nil {
			errs++
		} else {
			work++
		}
	}
	awaitSpawnMoves(h, beforeMoves+work)
	work += h.creates.Load() - beforeCreates
	work += h.models.Load() - beforeModels
	work += h.prompts.Load() - beforePrompts
	work += h.moves.Load() - beforeMoves
	errs += h.unexpected.Load() - beforeUnexpected
	return work, errs
}

// awaitSpawnMoves polls the move counter until the deferred setup goroutines
// have landed one move per successful Spawn. It observes the counter instead of
// consuming the channel tokens, which the benchmark drains after the timer
// stops. Yielding keeps the in-process transport free to finish the requests.
func awaitSpawnMoves(h *spawnHarnessBench, want int64) {
	for h.moves.Load() < want {
		runtime.Gosched()
	}
}

// waitSpawnMoves consumes one token per deferred move after the timer stops.
// Counting the channel removes the wall-clock deadline and makes a short run
// wait for exactly the moves it requested.
func waitSpawnMoves(h *spawnHarnessBench, want int64) {
	for i := int64(0); i < want; i++ {
		<-h.moved
	}
}

// TestSpawnWorkCountsAllMoves pins the published work to the final move count.
// Each Spawn serves a create, a model, a prompt and a deferred move, so ten
// successful spawns publish 5 * spawnCount work once every move has landed.
func TestSpawnWorkCountsAllMoves(t *testing.T) {
	h := newSpawnHarness(t, 1)
	work, errs := runHarnessSpawn(h)
	waitSpawnMoves(h, spawnCount)
	if got := h.moves.Load(); got != spawnCount {
		t.Fatalf("deferred moves = %d, want %d", got, spawnCount)
	}
	if want := int64(5 * spawnCount); work != want {
		t.Errorf("published work = %d, want %d", work, want)
	}
	if errs != 0 {
		t.Errorf("errors = %d, want 0", errs)
	}
}
