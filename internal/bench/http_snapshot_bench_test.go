package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app"
)

// httpSnapshotBench assembles the same snapshot that GET /api/v1/runs encodes.
// The rows are read once from the hot-path store outside the timed loop, so the
// measured work is SnapshotService.Snapshot plus json.Marshal, not SQL reads.
// Terminal runs and a fixed clock keep the encoded body byte-identical per pass.
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
	rows.active = terminalRuns(rows.active)
	rows.recent = terminalRuns(rows.recent)
	return &httpSnapshotBench{
		ctx: e.ctx,
		svc: app.SnapshotService{
			Store: rows,
			Now:   func() time.Time { return fixtureNow },
		},
	}
}

// runHTTPSnapshotAssembly returns the snapshot objects assembled and the
// assembly errors. Body bytes accumulate on the harness for b.ReportMetric.
func runHTTPSnapshotAssembly(h *httpSnapshotBench) (int64, int64) {
	out := assembleSnapshot(h.ctx, h.svc)
	h.bytes += len(out.body)
	return out.work, out.errs
}

// TestHTTPSnapshotAssemblyFixed proves that the injected clock and terminal
// fixture runs remove wall-clock drift from both the body bytes and the work.
func TestHTTPSnapshotAssemblyFixed(t *testing.T) {
	e := newSmallSnapshotEnv(t)
	rows, _, err := readAssemblyRows(e.ctx, e.store)
	if err != nil {
		t.Fatal(err)
	}
	original := rows.active[0].State
	rows.active = terminalRuns(rows.active)
	rows.recent = terminalRuns(rows.recent)
	if rows.active[0].State != "merged" {
		t.Fatalf("terminal run state = %q, want merged", rows.active[0].State)
	}
	if string(original) == "merged" {
		t.Fatal("test fixture unexpectedly started terminal")
	}
	svc := app.SnapshotService{Store: rows, Now: func() time.Time { return fixtureNow }}
	first := assembleSnapshot(e.ctx, svc)
	second := assembleSnapshot(e.ctx, svc)
	if first.errs != 0 || second.errs != 0 {
		t.Fatalf("assembly errs = %d and %d", first.errs, second.errs)
	}
	if first.work != second.work {
		t.Fatalf("work drifted: %d then %d", first.work, second.work)
	}
	if !bytes.Equal(first.body, second.body) {
		t.Fatal("assembled body changed between identical passes")
	}
	var snap app.Snapshot
	if err := json.Unmarshal(first.body, &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.GeneratedAt.Equal(fixtureNow) {
		t.Fatalf("generated_at = %s, want %s", snap.GeneratedAt, fixtureNow)
	}
}
