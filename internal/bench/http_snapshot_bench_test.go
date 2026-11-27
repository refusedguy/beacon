package bench

import (
	"context"
	"testing"

	"github.com/refusedguy/beacon/internal/app"
)

// httpSnapshotBench assembles the same snapshot that GET /api/v1/runs encodes.
// The rows are read once from the hot-path store outside the timed loop, so the
// measured work is SnapshotService.Snapshot plus json.Marshal, not SQL reads.
type httpSnapshotBench struct {
	ctx   context.Context
	svc   app.SnapshotService
	bytes int
}

func newHTTPSnapshotBench(b *testing.B, e *env) *httpSnapshotBench {
	b.Helper()
	rows, _, err := readAssemblyRows(e.ctx, e.store)
	if err != nil {
		b.Fatal(err)
	}
	return &httpSnapshotBench{ctx: e.ctx, svc: app.SnapshotService{Store: rows}}
}

// runHTTPSnapshotAssembly returns the snapshot objects assembled and the
// assembly errors. Body bytes accumulate on the harness for b.ReportMetric.
func runHTTPSnapshotAssembly(h *httpSnapshotBench) (int64, int64) {
	out := assembleSnapshot(h.ctx, h.svc)
	h.bytes += len(out.body)
	return out.work, out.errs
}
