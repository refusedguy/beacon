package store

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// HistogramDBLockWait names the write-lock wait histogram. The observe
// registry creates a histogram by name on first use, so this series reaches
// beacon inspect and the Prometheus exporter without a label.
const HistogramDBLockWait = "db_lock_wait_seconds"

// dbMetrics owns the counters, the wait timer and the observed writer wrapper
// used by DB's write path. DB keeps the pools and the queue; this type keeps
// the measurement.
type dbMetrics struct {
	// progress is non-nil only on a DB opened with OpenProduction. It makes
	// every call to Write go through ObservedPersist, so each committed write
	// advances persist_progress.seq and each failure records last_error.
	// Plain Open leaves it nil to keep the existing test fixtures' writer
	// behavior unchanged.
	progress *ObservedPersist

	// writeCount counts committed writer transactions. Tests use it to assert
	// that one observed Write costs one transaction, not two.
	writeCount atomic.Int64

	// lockWait sums the time writes spent waiting for the single writer and
	// the SQLite write lock. It has no labels, so it adds one fixed series.
	lockWait lockWaitStats
}

// lockWaitStats is the count and the summed seconds of write-lock waits.
// The writer records one observation per job; a reader may ask at any time.
type lockWaitStats struct {
	mu    sync.Mutex
	count uint64
	sum   float64
}

func (s *lockWaitStats) observe(seconds float64) {
	if seconds < 0 {
		return
	}
	s.mu.Lock()
	s.count++
	s.sum += seconds
	s.mu.Unlock()
}

// Stats returns a point-in-time copy of the count and the sum in seconds.
func (s *lockWaitStats) Stats() (uint64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.sum
}

// recordLockWait publishes one wait to the local stats and to the process-wide
// observe sink. The sink may be a no-op before the composition root installs a
// Recorder; the local stats still keep the measurement available to callers.
func (m *dbMetrics) recordLockWait(start time.Time) {
	if start.IsZero() {
		return
	}
	seconds := time.Since(start).Seconds()
	m.lockWait.observe(seconds)
	observe.Observe(HistogramDBLockWait, seconds)
}

// countWrite increments the committed-writer counter after a successful commit.
func (m *dbMetrics) countWrite() { m.writeCount.Add(1) }

// WriteCount reports how many writer transactions have committed. It is an
// observability counter used to prove the observed write path stays at one
// transaction per Write (BCX-37).
func (m *dbMetrics) WriteCount() int64 { return m.writeCount.Load() }

// LockWaitStats reports the count and the summed seconds of write-lock waits.
// The count includes writes whose transaction failed to commit, because the
// wait happened before that outcome was known.
func (m *dbMetrics) LockWaitStats() (uint64, float64) { return m.lockWait.Stats() }
