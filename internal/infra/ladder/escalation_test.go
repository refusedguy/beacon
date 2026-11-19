package ladder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
	_ "modernc.org/sqlite"
)

// testDB is the shared persistence double for both ladder test files. The
// embedded *store.DB drives the same production code path as the transitions
// tests; EscalatedCount adds the one read the escalator needs.
type testDB struct {
	*store.DB
}

func newTestDB(t *testing.T) *testDB {
	t.Helper()
	d, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &testDB{DB: d}
}

func (d *testDB) EscalatedCount(ctx context.Context) (int, error) {
	if _, err := d.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			CREATE TABLE IF NOT EXISTS escalation_stats
			  (id INTEGER PRIMARY KEY CHECK (id = 1),
			   escalated INTEGER NOT NULL DEFAULT 0)`)
		return err
	}); err != nil {
		return 0, err
	}
	var n int
	err := d.DB.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, escalatedCountSQL).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return n, err
}

type stubEpisodes struct{ items []Episode }

func (s *stubEpisodes) StagedEpisodes() ([]Episode, error) { return s.items, nil }

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type sendRecord struct {
	target  string
	pointer domain.Pointer
}

type recordingSender struct{ sends []sendRecord }

func (s *recordingSender) Send(_ context.Context, target string, p domain.Pointer) error {
	s.sends = append(s.sends, sendRecord{target: target, pointer: p})
	return nil
}

type harness struct {
	esc    *Escalator
	db     *testDB
	src    *stubEpisodes
	sender *recordingSender
	path   string
	staged time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := newTestDB(t)
	src := &stubEpisodes{}
	sender := &recordingSender{}
	path := filepath.Join(t.TempDir(), "ESCALATIONS.md")
	staged := time.Date(2026, 11, 19, 11, 0, 0, 0, time.UTC)
	esc := NewEscalator(src, d, sender, fixedClock{now: staged}, path)
	return &harness{esc: esc, db: d, src: src, sender: sender, path: path, staged: staged}
}

func (h *harness) at(d time.Duration) { h.esc.clock = fixedClock{now: h.staged.Add(d)} }

func (h *harness) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", h.path, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (h *harness) escalated(t *testing.T) int {
	t.Helper()
	n, err := h.esc.EscalatedCount(context.Background())
	if err != nil {
		t.Fatalf("escalated count: %v", err)
	}
	return n
}

func newEpisode(t *testing.T, d *testDB, watchID string, staged time.Time, mutate func(*domain.Obligation)) Episode {
	t.Helper()
	o := domain.Obligation{
		ID:             watchID + ":1:orch",
		WatchID:        watchID,
		Revision:       1,
		SubscriberID:   "orch",
		VerdictPointer: domain.VerdictReady,
		StagedAt:       staged,
	}
	if mutate != nil {
		mutate(&o)
	}
	payload, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal obligation: %v", err)
	}
	err = d.DB.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO obligations
			  (id, watch_id, revision, subscriber_id, verdict_pointer, payload)
			VALUES (?, ?, ?, ?, ?, ?)`,
			o.ID, o.WatchID, o.Revision, o.SubscriberID, string(o.VerdictPointer), payload)
		return err
	})
	if err != nil {
		t.Fatalf("insert obligation: %v", err)
	}
	w := domain.Watch{
		ID:       watchID,
		Repo:     "o/r",
		PR:       7,
		Revision: 1,
		Verdict:  domain.VerdictReady,
		Subscribers: []domain.Subscriber{
			{ID: "orch", Role: domain.RoleOrchestrator, Target: "orchestrator"},
		},
	}
	return Episode{Watch: w, Obligation: o, ConfiguredTarget: "configured"}
}

func storedObligation(t *testing.T, d *testDB, id string) domain.Obligation {
	t.Helper()
	var payload []byte
	if err := d.DB.Write(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload)
	}); err != nil {
		t.Fatalf("read obligation %s: %v", id, err)
	}
	var o domain.Obligation
	if err := json.Unmarshal(payload, &o); err != nil {
		t.Fatalf("decode obligation: %v", err)
	}
	return o
}

func TestNothingBeforeRung1(t *testing.T) {
	h := newHarness(t)
	h.src.items = []Episode{newEpisode(t, h.db, "watch-1", h.staged, nil)}
	h.at(10 * time.Minute)

	got, err := h.esc.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 0 {
		t.Fatalf("committed = %d, want 0", got)
	}
	if len(h.sender.sends) != 0 {
		t.Fatalf("sends = %d, want 0", len(h.sender.sends))
	}
	if lines := h.lines(t); len(lines) != 0 {
		t.Fatalf("ESCALATIONS.md lines = %v, want none", lines)
	}
	if n := h.escalated(t); n != 0 {
		t.Fatalf("stats.escalated = %d, want 0", n)
	}
}

func TestRung1SendsPointerOnlyAtFifteenMinutes(t *testing.T) {
	h := newHarness(t)
	ep := newEpisode(t, h.db, "watch-1", h.staged, nil)
	h.src.items = []Episode{ep}
	h.at(Rung1After)

	got, err := h.esc.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 0 {
		t.Fatalf("committed = %d, want 0 for rung 1", got)
	}
	if len(h.sender.sends) != 1 {
		t.Fatalf("sends = %d, want 1", len(h.sender.sends))
	}
	send := h.sender.sends[0]
	if send.target != "orchestrator" {
		t.Errorf("target = %q, want orchestrator subscriber", send.target)
	}
	want := domain.Pointer{WatchID: "watch-1", ObligationID: ep.Obligation.ID, Revision: 1}
	if send.pointer != want {
		t.Errorf("pointer = %+v, want %+v", send.pointer, want)
	}
	if lines := h.lines(t); len(lines) != 0 {
		t.Fatalf("rung 1 must not write ESCALATIONS.md, got %v", lines)
	}
	if n := h.escalated(t); n != 0 {
		t.Fatalf("rung 1 must not bump stats.escalated, got %d", n)
	}
}

func TestRung1UsesConfiguredTargetWithoutOrchestrator(t *testing.T) {
	h := newHarness(t)
	ep := newEpisode(t, h.db, "watch-1", h.staged, nil)
	ep.Watch.Subscribers = nil
	h.src.items = []Episode{ep}
	h.at(Rung1After)

	if _, err := h.esc.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(h.sender.sends) != 1 || h.sender.sends[0].target != "configured" {
		t.Fatalf("sends = %+v, want one send to configured target", h.sender.sends)
	}
}

func TestRung2WritesOneLineAndCounterAtThirtyMinutes(t *testing.T) {
	h := newHarness(t)
	ep := newEpisode(t, h.db, "watch-1", h.staged, nil)
	h.src.items = []Episode{ep}
	h.at(Rung2After)

	got, err := h.esc.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 1 {
		t.Fatalf("committed = %d, want 1", got)
	}
	lines := h.lines(t)
	if len(lines) != 1 {
		t.Fatalf("ESCALATIONS.md lines = %v, want exactly one", lines)
	}
	if !strings.Contains(lines[0], "episode="+ep.Key()) {
		t.Errorf("line %q missing episode key %q", lines[0], ep.Key())
	}
	if n := h.escalated(t); n != 1 {
		t.Errorf("stats.escalated = %d, want 1", n)
	}
	o := storedObligation(t, h.db, ep.Obligation.ID)
	if o.EscalationLevel != rungTwo {
		t.Errorf("escalation level = %d, want %d", o.EscalationLevel, rungTwo)
	}
	if want := h.staged.Add(Rung2After); !o.EscalatedAt.Equal(want) {
		t.Errorf("escalatedAt = %s, want %s", o.EscalatedAt, want)
	}
	if len(h.sender.sends) != 1 {
		t.Errorf("sends = %d, want 1", len(h.sender.sends))
	}
}

func TestRung2IsOncePerEpisode(t *testing.T) {
	h := newHarness(t)
	h.src.items = []Episode{newEpisode(t, h.db, "watch-1", h.staged, nil)}

	h.at(Rung2After)
	if got, err := h.esc.Run(context.Background()); err != nil || got != 1 {
		t.Fatalf("first run: committed=%d err=%v", got, err)
	}

	h.at(45 * time.Minute)
	if got, err := h.esc.Run(context.Background()); err != nil || got != 0 {
		t.Fatalf("repeat run: committed=%d err=%v", got, err)
	}
	if lines := h.lines(t); len(lines) != 1 {
		t.Fatalf("ESCALATIONS.md lines = %v, want exactly one", lines)
	}
	if n := h.escalated(t); n != 1 {
		t.Errorf("stats.escalated = %d, want 1 after repeat", n)
	}
	if len(h.sender.sends) != 1 {
		t.Errorf("sends = %d, want 1 after repeat", len(h.sender.sends))
	}
}

func TestClaimedStopsBothRungs(t *testing.T) {
	h := newHarness(t)
	ep := newEpisode(t, h.db, "watch-1", h.staged, func(o *domain.Obligation) {
		o.ClaimedAt = h.staged.Add(5 * time.Minute)
	})
	h.src.items = []Episode{ep}
	h.at(60 * time.Minute)

	got, err := h.esc.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 0 || len(h.sender.sends) != 0 {
		t.Fatalf("claimed episode: committed=%d sends=%d, want 0/0", got, len(h.sender.sends))
	}
	if lines := h.lines(t); len(lines) != 0 {
		t.Fatalf("claimed episode wrote ESCALATIONS.md: %v", lines)
	}
	if n := h.escalated(t); n != 0 {
		t.Errorf("claimed episode stats.escalated = %d, want 0", n)
	}
}

func TestReadStopsBothRungs(t *testing.T) {
	h := newHarness(t)
	ep := newEpisode(t, h.db, "watch-1", h.staged, func(o *domain.Obligation) {
		o.ReadAt = h.staged.Add(5 * time.Minute)
	})
	h.src.items = []Episode{ep}
	h.at(60 * time.Minute)

	got, err := h.esc.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 0 || len(h.sender.sends) != 0 {
		t.Fatalf("read episode: committed=%d sends=%d, want 0/0", got, len(h.sender.sends))
	}
	if lines := h.lines(t); len(lines) != 0 {
		t.Fatalf("read episode wrote ESCALATIONS.md: %v", lines)
	}
	if n := h.escalated(t); n != 0 {
		t.Errorf("read episode stats.escalated = %d, want 0", n)
	}
}

func TestEachEpisodeGetsItsOwnLine(t *testing.T) {
	h := newHarness(t)
	one := newEpisode(t, h.db, "watch-1", h.staged, nil)
	two := newEpisode(t, h.db, "watch-2", h.staged, nil)
	h.src.items = []Episode{one, two}
	h.at(Rung2After)

	got, err := h.esc.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 2 {
		t.Fatalf("committed = %d, want 2", got)
	}
	if lines := h.lines(t); len(lines) != 2 {
		t.Fatalf("ESCALATIONS.md lines = %v, want two", lines)
	}
	if n := h.escalated(t); n != 2 {
		t.Errorf("stats.escalated = %d, want 2", n)
	}
}
