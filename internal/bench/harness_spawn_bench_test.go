package bench

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/refusedguy/beacon/harnesses/opencode"
	"github.com/refusedguy/beacon/pkg/core"
)

// spawnCount bounds the smoke work. Ten spawns cover the create, model write,
// prompt and deferred move without a slow fixture.
const spawnCount = 10

// spawnHarnessBench holds one OpenCode harness pointed at a loopback server.
// Counters are read before and after each batch so every row reports the
// requests it served. The move channel lets the caller wait for deferred setup
// after the benchmark timer stops.
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
	var creates, models, moves, prompts, unexpected, next atomic.Int64
	moved := make(chan struct{}, spawnCount*2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/api/session":
			creates.Add(1)
			id := fmt.Sprintf("ses_%d", next.Add(1))
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/model"):
			models.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/move"):
			moves.Add(1)
			select {
			case moved <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/prompt"):
			prompts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"delivery":"steer"}`))
		default:
			unexpected.Add(1)
			http.NotFound(w, r)
		}
	}))
	b.Cleanup(srv.Close)
	return &spawnHarnessBench{
		harness:    opencode.New(opencode.Config{Endpoint: srv.URL, Model: "bench-model"}),
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
// requests they served, including the deferred move; errors are Spawn failures
// and requests the fixture does not model.
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
	work += h.creates.Load() - beforeCreates
	work += h.models.Load() - beforeModels
	work += h.prompts.Load() - beforePrompts
	work += h.moves.Load() - beforeMoves
	errs += h.unexpected.Load() - beforeUnexpected
	return work, errs
}

// waitSpawnMoves waits for the deferred moves after the timer stops. A bounded
// deadline keeps a stalled loopback server from hanging the benchmark.
func waitSpawnMoves(h *spawnHarnessBench, want int64) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for h.moves.Load() < want {
		select {
		case <-h.moved:
		case <-deadline.C:
			return
		}
	}
}
