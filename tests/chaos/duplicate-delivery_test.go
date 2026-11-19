// Package chaos holds the P4 chaos harness for duplicate and indeterminate
// delivery. The filename ends in _test.go on purpose: Go only registers
// TestXxx functions from such files, so a JS-style "*.test.go" name would
// compile as a plain source file and never run under `go test ./...` (ADR 0022
// requires the test to actually run in CI).
package chaos

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

const (
	// proofWindow scales ADR 0017's 15-minute proof window down to CI time.
	proofWindow = 30 * time.Millisecond
	// startupBudget is the restart limit from WORKER-PROMPT §5.
	startupBudget = 5 * time.Second
)

// chaosEnv is the isolated environment from step 1 of the card.
//
// state.db is the T1.2 SQLite store. receipts.jsonl is the file boundary the
// T3.1 plugin writes and T3.6 imports. deliverPointer below stands in for the
// T3.2 pointer channel. The ladder and escalation code from T3.9/T3.10 is not
// imported here so this package compiles on main today; once those tasks land,
// their real implementations replace the small fakes at the marked points.
type chaosEnv struct {
	dir          string
	dbPath       string
	receiptsPath string
	db           *store.DB
}

func newChaosEnv(t *testing.T) *chaosEnv {
	t.Helper()
	dir := t.TempDir()
	env := &chaosEnv{
		dir:          dir,
		dbPath:       filepath.Join(dir, "state.db"),
		receiptsPath: filepath.Join(dir, "receipts.jsonl"),
	}
	env.reopen(t)
	return env
}

func (e *chaosEnv) reopen(t *testing.T) {
	t.Helper()
	db, err := store.Open(e.dbPath)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", e.dbPath, err)
	}
	e.db = db
}

func (e *chaosEnv) close(t *testing.T) {
	t.Helper()
	if err := e.db.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
}

func (e *chaosEnv) seedObligation(t *testing.T, watchID string, revision uint64, subscriberID string) domain.Obligation {
	t.Helper()
	id := domain.ObligationKey(watchID, revision, subscriberID)
	o := domain.Obligation{
		ID:             id,
		WatchID:        watchID,
		Revision:       revision,
		SubscriberID:   subscriberID,
		VerdictPointer: domain.VerdictReady,
		StagedAt:       time.Now().UTC(),
	}
	if err := e.db.SaveObligation(o); err != nil {
		t.Fatalf("SaveObligation(%s): %v", id, err)
	}
	return o
}

// deliverPointer appends one receipt line and imports it. ImportReceipts is
// idempotent on (obligationID, attempt), so sending the same pointer twice
// applies one effect and reports the second delivery as a duplicate.
func (e *chaosEnv) deliverPointer(t *testing.T, o domain.Obligation, attempt int, outcome domain.AttemptOutcome, at time.Time) store.ImportReceiptsResult {
	t.Helper()
	line := fmt.Sprintf(`{"obligationID":%q,"attempt":%d,"outcome":%q,"at":%q}`,
		o.ID, attempt, string(outcome), at.UTC().Format(time.RFC3339Nano))
	f, err := os.OpenFile(e.receiptsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open receipts: %v", err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write receipt: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close receipts: %v", err)
	}
	res, err := e.db.ImportReceipts(e.receiptsPath)
	if err != nil {
		t.Fatalf("ImportReceipts: %v", err)
	}
	return res
}

// decideResend is the T3.2/T3.9 branch at step 3: only a transport-failed
// receipt schedules another pointer. No receipt is indeterminate (ADR 0014)
// and never schedules one, so an absent receipt cannot double the effect.
func decideResend(_ domain.Obligation, receipts []store.Receipt) bool {
	if len(receipts) == 0 {
		return false
	}
	last := receipts[len(receipts)-1]
	return last.Outcome == domain.OutcomeTransportFailed
}

// escalationLedger records one escalation per episode. T3.10 will supply the
// durable version; here the key is the obligation, which already encodes the
// episode as (watchID, revision, subscriberID) through domain.ObligationKey.
type escalationLedger struct {
	mu       sync.Mutex
	episodes map[string]int
}

func newEscalationLedger() *escalationLedger {
	return &escalationLedger{episodes: make(map[string]int)}
}

func (l *escalationLedger) fire(episode string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, seen := l.episodes[episode]; !seen {
		l.episodes[episode] = 1
	}
}

func (l *escalationLedger) count(episode string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.episodes[episode]
}

// TestDuplicateDeliverySingleEffect is step 2 of the card: delivering the
// same pointer twice produces one recipient action, keyed by
// (watchID, revision, subscriberID), hence one wake and one merge.
func TestDuplicateDeliverySingleEffect(t *testing.T) {
	env := newChaosEnv(t)
	defer env.close(t)

	o := env.seedObligation(t, "watch-A", 1, "sub-orchestrator")
	wantKey := domain.ObligationKey("watch-A", 1, "sub-orchestrator")
	if o.ID != wantKey {
		t.Fatalf("obligation id = %q, want %q", o.ID, wantKey)
	}
	if other := domain.ObligationKey("watch-A", 1, "sub-executor"); other == o.ID {
		t.Fatal("different subscriberID produced the same idempotency key")
	}

	first := env.deliverPointer(t, o, 1, domain.OutcomePosted, time.Now())
	second := env.deliverPointer(t, o, 1, domain.OutcomePosted, time.Now())

	// Each newly imported receipt is one wake and one orchestrator merge.
	// ImportReceipts reprocesses the whole file, so its Duplicates field is
	// cumulative over lines, not per delivery. Two deliveries minus one
	// applied effect gives the number of duplicate deliveries that were
	// dropped by the idempotency key.
	effects := first.Imported + second.Imported
	if effects != 1 {
		t.Fatalf("effects = %d, want 1", effects)
	}
	dropped := 2 - effects
	if dropped != 1 {
		t.Fatalf("duplicate deliveries dropped = %d, want 1", dropped)
	}
	receipts, err := env.db.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("receipts = %d, want 1", receipts)
	}
	stored, err := env.db.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if stored.DeliveredAt.IsZero() {
		t.Fatal("posted receipt did not set deliveredAt exactly once")
	}
	if len(stored.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(stored.Attempts))
	}

	t.Logf("duplicate delivery: deliveries=2 effects=%d duplicates=%d receipts=%d merges=%d wakes=%d",
		effects, dropped, receipts, effects, effects)
}

// TestIndeterminateNoRetry is step 3 of the card: an obligation with no
// receipt is indeterminate, is never retried, and escalates exactly once.
func TestIndeterminateNoRetry(t *testing.T) {
	env := newChaosEnv(t)
	defer env.close(t)

	o := env.seedObligation(t, "watch-B", 1, "sub-orphan")

	// No receipt is written for this obligation, so receipts stays empty.
	var receipts []store.Receipt
	resends := 0
	deadline := time.Now().Add(proofWindow)
	for time.Now().Before(deadline) {
		if decideResend(o, receipts) {
			resends++
			attempt := len(receipts) + 1
			env.deliverPointer(t, o, attempt, domain.OutcomeTransportFailed, time.Now())
			receipts = append(receipts, store.Receipt{
				ObligationID: o.ID,
				Attempt:      attempt,
				Outcome:      domain.OutcomeTransportFailed,
			})
		}
		time.Sleep(proofWindow / 10)
	}
	if resends != 0 {
		t.Fatalf("resends = %d, want 0", resends)
	}
	count, err := env.db.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("receipts = %d, want 0", count)
	}
	stored, err := env.db.Obligation(o.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}
	if !stored.DeliveredAt.IsZero() {
		t.Fatal("indeterminate obligation was marked delivered")
	}
	pending, err := env.db.PendingObligations()
	if err != nil {
		t.Fatalf("PendingObligations: %v", err)
	}
	found := false
	for _, p := range pending {
		if p.ID == o.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("indeterminate obligation is no longer pending")
	}

	// After the proof window the missing proof escalates once per episode.
	// The second fire models a repeated trigger and must be deduplicated.
	esc := newEscalationLedger()
	esc.fire(o.ID)
	esc.fire(o.ID)
	if got := esc.count(o.ID); got != 1 {
		t.Fatalf("escalations = %d, want 1", got)
	}

	t.Logf("indeterminate: proof_window=%s resends=%d receipts=%d deliveries=%d escalations=%d",
		proofWindow, resends, count, 0, esc.count(o.ID))
}

// TestRestartPreservesObligations is step 4 of the card: a restart in the
// middle of the ladder keeps obligations and stays within the 5 s budget,
// and a duplicate pointer after the restart still has a single effect.
func TestRestartPreservesObligations(t *testing.T) {
	env := newChaosEnv(t)
	o1 := env.seedObligation(t, "watch-C", 1, "sub-a")
	o2 := env.seedObligation(t, "watch-C", 1, "sub-b")

	first := env.deliverPointer(t, o1, 1, domain.OutcomePosted, time.Now())
	if first.Imported != 1 {
		t.Fatalf("first import = %d, want 1", first.Imported)
	}
	beforeReceipts, err := env.db.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount: %v", err)
	}
	beforePending, err := env.db.PendingObligations()
	if err != nil {
		t.Fatalf("PendingObligations: %v", err)
	}
	beforeDelivered, err := env.db.Obligation(o1.ID)
	if err != nil {
		t.Fatalf("Obligation: %v", err)
	}

	env.close(t)
	start := time.Now()
	env.reopen(t)
	startup := time.Since(start)
	defer env.close(t)

	if startup > startupBudget {
		t.Fatalf("restart took %s, want <= %s", startup, startupBudget)
	}
	afterReceipts, err := env.db.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount after restart: %v", err)
	}
	if afterReceipts != beforeReceipts {
		t.Fatalf("receipts after restart = %d, want %d", afterReceipts, beforeReceipts)
	}
	afterPending, err := env.db.PendingObligations()
	if err != nil {
		t.Fatalf("PendingObligations after restart: %v", err)
	}
	if len(afterPending) != len(beforePending) {
		t.Fatalf("pending after restart = %d, want %d", len(afterPending), len(beforePending))
	}
	for _, want := range []string{o1.ID, o2.ID} {
		if _, err := env.db.Obligation(want); err != nil {
			t.Fatalf("obligation %s lost on restart: %v", want, err)
		}
	}
	afterDelivered, err := env.db.Obligation(o1.ID)
	if err != nil {
		t.Fatalf("Obligation after restart: %v", err)
	}
	if !afterDelivered.DeliveredAt.Equal(beforeDelivered.DeliveredAt) {
		t.Fatalf("deliveredAt changed on restart: %v -> %v", beforeDelivered.DeliveredAt, afterDelivered.DeliveredAt)
	}

	// The idempotency key lives in the store, so it survives the restart.
	second := env.deliverPointer(t, o1, 1, domain.OutcomePosted, time.Now())
	if second.Imported != 0 || second.Duplicates < 1 {
		t.Fatalf("post-restart delivery = %+v, want 0 imported and at least one duplicate", second)
	}
	finalReceipts, err := env.db.ReceiptCount()
	if err != nil {
		t.Fatalf("ReceiptCount after repeat: %v", err)
	}
	if finalReceipts != beforeReceipts {
		t.Fatalf("receipts after repeat = %d, want %d", finalReceipts, beforeReceipts)
	}

	t.Logf("restart: duration=%s budget=%s obligations=2 pending=%d receipts=%d post_restart_effects=0",
		startup, startupBudget, len(afterPending), finalReceipts)
}
