// Package ladder: rung 1 and rung 2 escalation of a staged wake episode.
//
// ADR 0017 puts rung 1 at 15 minutes from staged and rung 2 at 30 minutes.
// Rung 1 sends a pointer only, to the orchestrator subscriber of the watch or
// to a configured target. Rung 2 writes exactly one line to ESCALATIONS.md and
// increments stats.escalated in the same transaction as the ladder marker on
// the obligation. Repeating either rung for the same episode is a no-op.
//
// claimed or read stops both timers. Time passing alone never closes a watch
// (ADR 0019), so this file writes no verdict and no close reason.
//
// The package deliberately depends only on internal/domain and the standard
// library. The SQL adapter is injected through EscalationDB, and the staged
// markers produced by the T3.9 transitions are injected through EpisodeReader.
package ladder

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// Rung1After is the proof window from ADR 0017: 15 minutes from staged.
	Rung1After = 15 * time.Minute
	// Rung2After is the second rung from ADR 0017: 30 minutes from staged.
	Rung2After = 30 * time.Minute

	rungOne = 1
	rungTwo = 2
)

// Schema for the persisted ladder state. It is created on first use, the same
// way observability.go creates persist_progress, because the migration corpus
// belongs to the store package and this adapter owns no migration file.
const (
	ensureEscalationTablesSQL = `
		CREATE TABLE IF NOT EXISTS escalation_rungs (
		  episode_key TEXT NOT NULL,
		  rung        INTEGER NOT NULL,
		  at          INTEGER NOT NULL,
		  PRIMARY KEY (episode_key, rung)
		);
		CREATE TABLE IF NOT EXISTS escalation_lines (
		  episode_key TEXT PRIMARY KEY,
		  line        TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS escalation_stats (
		  id        INTEGER PRIMARY KEY CHECK (id = 1),
		  escalated INTEGER NOT NULL DEFAULT 0
		);
		INSERT OR IGNORE INTO escalation_stats (id, escalated) VALUES (1, 0);
	`

	escalatedCountSQL = `SELECT escalated FROM escalation_stats WHERE id = 1`
)

// Episode is one staged obligation together with its watch. Obligation.ID is
// derived from watch id, revision and subscriber id, so it is the stable
// episode key used for idempotency. The T3.9 transitions write the staged
// marker; an adapter over that journal implements EpisodeReader.
type Episode struct {
	Watch            domain.Watch
	Obligation       domain.Obligation
	ConfiguredTarget string
}

// Key is the episode key written to escalation_rungs and ESCALATIONS.md.
func (e Episode) Key() string { return e.Obligation.ID }

// EpisodeReader lists staged episodes that still have a live obligation.
type EpisodeReader interface {
	StagedEpisodes() ([]Episode, error)
}

// PointerSender delivers a rung 1 pointer to one target. The pointer carries
// identifiers only, never result content (ADR 0017).
type PointerSender interface {
	Send(ctx context.Context, target string, p domain.Pointer) error
}

// EscalationDB is the narrow persistence port. Write is the single writer that
// commits a transaction; EscalatedCount is a read of stats.escalated. *store.DB
// satisfies Write, and a thin adapter supplies EscalatedCount.
type EscalationDB interface {
	Write(ctx context.Context, fn func(*sql.Tx) error) error
	EscalatedCount(ctx context.Context) (int, error)
}

// Escalator advances staged episodes through rung 1 and rung 2.
type Escalator struct {
	episodes EpisodeReader
	db       EscalationDB
	sender   PointerSender
	clock    domain.Clock
	path     string
	rung1    time.Duration
	rung2    time.Duration
}

// NewEscalator builds an escalator with the ADR 0017 windows. path is the
// location of ESCALATIONS.md, normally next to events.jsonl.
func NewEscalator(episodes EpisodeReader, db EscalationDB, sender PointerSender, clock domain.Clock, path string) *Escalator {
	return &Escalator{
		episodes: episodes,
		db:       db,
		sender:   sender,
		clock:    clock,
		path:     path,
		rung1:    Rung1After,
		rung2:    Rung2After,
	}
}

// Run advances every staged episode to the rung its age warrants. It returns
// the number of rung 2 escalations committed in this pass. A failed rung 1
// send is reported, but the rung 1 marker is kept so the pointer is not sent
// twice (ADR 0017 has no retry for this branch).
func (e *Escalator) Run(ctx context.Context) (int, error) {
	episodes, err := e.episodes.StagedEpisodes()
	if err != nil {
		return 0, fmt.Errorf("escalation: read staged episodes: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := e.clock.Now()
	committed := 0
	for _, ep := range episodes {
		if ladderStopped(ep) || episodeActive(ep, now) {
			continue
		}
		age := now.Sub(ep.Obligation.StagedAt)
		if age < e.rung1 {
			continue
		}

		sent, err := e.markRung(ctx, ep.Key(), rungOne, now)
		if err != nil {
			return committed, err
		}
		if sent {
			if target := rung1Target(ep); target != "" {
				p := domain.Pointer{
					WatchID:      ep.Watch.ID,
					ObligationID: ep.Obligation.ID,
					Revision:     ep.Obligation.Revision,
				}
				if err := e.sender.Send(ctx, target, p); err != nil {
					return committed, fmt.Errorf("escalation: rung 1 pointer: %w", err)
				}
			}
		}

		if age < e.rung2 {
			continue
		}
		line := escalationLine(ep, now)
		done, err := e.commitRung2(ctx, ep, now, line)
		if err != nil {
			return committed, err
		}
		if done {
			committed++
		}
	}
	return committed, nil
}

// EscalatedCount returns stats.escalated without writing.
func (e *Escalator) EscalatedCount(ctx context.Context) (int, error) {
	return e.db.EscalatedCount(ctx)
}

// ladderStopped reports whether claimed or read already stopped the timers.
func ladderStopped(ep Episode) bool {
	return !ep.Obligation.ClaimedAt.IsZero() || !ep.Obligation.ReadAt.IsZero()
}

// episodeActive reports whether a subscriber showed fresh activity inside the
// evidence window. Claimed and read are the hard proofs; fresh activity is the
// third signal checked at 15 minutes. An episode with any of the three is
// engaged, so neither rung fires (ADR 0017, WORKER-PROMPT §4.5).
func episodeActive(ep Episode, now time.Time) bool {
	for _, s := range ep.Watch.Subscribers {
		if !s.LastActivity.IsZero() && s.LastActivity.After(now.Add(-Rung1After)) {
			return true
		}
	}
	return false
}

// rung1Target prefers the orchestrator subscriber. The configured target is
// the fallback when the watch has no orchestrator subscriber.
func rung1Target(ep Episode) string {
	for _, s := range ep.Watch.Subscribers {
		if s.Role == domain.RoleOrchestrator && s.Target != "" {
			return s.Target
		}
	}
	return ep.ConfiguredTarget
}

// pointer-only payloads carry no content, so the line holds only identifiers.
func escalationLine(ep Episode, at time.Time) string {
	return fmt.Sprintf("%s episode=%s watch=%s revision=%d rung=2 staged=%s escalated=%s",
		at.UTC().Format(time.RFC3339),
		ep.Key(),
		ep.Watch.ID,
		ep.Obligation.Revision,
		ep.Obligation.StagedAt.UTC().Format(time.RFC3339),
		at.UTC().Format(time.RFC3339),
	)
}

// markRung records that a rung fired. It returns true only for the first call
// with the same (episode key, rung) pair.
func (e *Escalator) markRung(ctx context.Context, key string, rung int, at time.Time) (bool, error) {
	first := false
	err := e.db.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureEscalationTables(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO escalation_rungs (episode_key, rung, at)
			VALUES (?, ?, ?)`, key, rung, at.UnixNano())
		if err != nil {
			return fmt.Errorf("escalation: mark rung %d: %w", rung, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		first = n == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("escalation: mark rung %d: %w", rung, err)
	}
	return first, nil
}

// commitRung2 writes the rung 2 marker, the obligation marker, stats.escalated
// and the ESCALATIONS.md line in one transaction. The UNIQUE key on
// escalation_rungs makes a repeated escalation of the same episode a no-op.
func (e *Escalator) commitRung2(ctx context.Context, ep Episode, at time.Time, line string) (bool, error) {
	committed := false
	err := e.db.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureEscalationTables(tx); err != nil {
			return err
		}
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO escalation_rungs (episode_key, rung, at)
			VALUES (?, ?, ?)`, ep.Key(), rungTwo, at.UnixNano())
		if err != nil {
			return fmt.Errorf("escalation: mark rung 2: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		committed = true

		if _, err := tx.Exec(`
			INSERT INTO escalation_lines (episode_key, line) VALUES (?, ?)`,
			ep.Key(), line); err != nil {
			return fmt.Errorf("escalation: record line: %w", err)
		}
		if _, err := tx.Exec(`
			UPDATE escalation_stats SET escalated = escalated + 1 WHERE id = 1`); err != nil {
			return fmt.Errorf("escalation: bump counter: %w", err)
		}
		if err := markObligationEscalated(tx, ep, at); err != nil {
			return err
		}
		// The file check makes the append idempotent even if a previous
		// transaction wrote the line and then rolled back.
		if err := appendEscalationLineOnce(e.path, ep.Key(), line); err != nil {
			return fmt.Errorf("escalation: append %s: %w", e.path, err)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("escalation: commit rung 2: %w", err)
	}
	return committed, nil
}

func ensureEscalationTables(tx *sql.Tx) error {
	if _, err := tx.Exec(ensureEscalationTablesSQL); err != nil {
		return fmt.Errorf("escalation: ensure tables: %w", err)
	}
	return nil
}

// markObligationEscalated updates the persisted obligation marker in the same
// transaction. An obligation that is not in the store yet needs no marker.
func markObligationEscalated(tx *sql.Tx, ep Episode, at time.Time) error {
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, ep.Obligation.ID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("escalation: read obligation: %w", err)
	}
	var o domain.Obligation
	if err := json.Unmarshal(payload, &o); err != nil {
		return fmt.Errorf("escalation: decode obligation: %w", err)
	}
	o.EscalatedAt = at
	o.EscalationLevel = rungTwo
	updated, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("escalation: encode obligation: %w", err)
	}
	if _, err := tx.Exec(`UPDATE obligations SET payload = ? WHERE id = ?`, updated, o.ID); err != nil {
		return fmt.Errorf("escalation: write obligation: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Production adapters over the single-writer store port.
//
// The escalation core above depends on narrow ports only. These adapters bind
// those ports to the same Writer the transitions use, so a process needs one
// *store.DB handle for the whole ladder. The adapters issue SQL through Writer
// and decode the domain payloads themselves; this package still imports no
// sibling infra package.
// ---------------------------------------------------------------------------

// EscalationStore adapts a Writer to EscalationDB. EscalatedCount reads the
// same escalation_stats row the rung 2 transaction increments, through the
// single writer, so no second connection or process-local counter is needed.
type EscalationStore struct {
	writer Writer
}

// NewEscalationStore builds an EscalationDB over w.
func NewEscalationStore(w Writer) *EscalationStore {
	return &EscalationStore{writer: w}
}

// Write delegates to the shared single writer.
func (s *EscalationStore) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	if s == nil || s.writer == nil {
		return errors.New("escalation: escalation store writer is nil")
	}
	return s.writer.Write(ctx, fn)
}

// EscalatedCount returns stats.escalated without writing a new marker.
func (s *EscalationStore) EscalatedCount(ctx context.Context) (int, error) {
	if s == nil || s.writer == nil {
		return 0, errors.New("escalation: escalation store writer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	n := 0
	err := s.writer.Write(ctx, func(tx *sql.Tx) error {
		if err := ensureEscalationTables(tx); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, escalatedCountSQL).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("escalation: read escalated count: %w", err)
	}
	return n, nil
}

// StagedEpisodeStore adapts a Writer to EpisodeReader. It reads staged
// obligations and their Watches through the shared writer, using the latest
// ladder marker as the definition of "still staged". The configured target is
// the project orchestrator fallback used when a Watch has no orchestrator
// subscriber (rung 1).
type StagedEpisodeStore struct {
	writer           Writer
	configuredTarget func() string
}

// NewStagedEpisodeStore builds an EpisodeReader over w. configuredTarget may
// be nil, in which case rung 1 has no fallback target.
func NewStagedEpisodeStore(w Writer, configuredTarget func() string) *StagedEpisodeStore {
	return &StagedEpisodeStore{writer: w, configuredTarget: configuredTarget}
}

// StagedEpisodes lists obligations whose latest ladder marker is active or
// escalated. Closed Watches are excluded: a terminal verdict has no live
// episode to escalate (ADR 0019).
func (s *StagedEpisodeStore) StagedEpisodes() ([]Episode, error) {
	if s == nil || s.writer == nil {
		return nil, errors.New("escalation: episode store writer is nil")
	}
	var episodes []Episode
	err := s.writer.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureJournal(tx); err != nil {
			return err
		}
		rows, err := tx.Query(`
			SELECT o.payload, w.payload FROM obligations o
			JOIN watches w ON w.id = o.watch_id
			JOIN ladder_journal j ON j.obligation_id = o.id
			JOIN (
			  SELECT obligation_id, MAX(seq) AS seq FROM ladder_journal
			  GROUP BY obligation_id
			) latest ON j.seq = latest.seq
			WHERE j.status IN (?, ?) AND w.status != ?
			ORDER BY o.id`,
			string(StatusActive), string(StatusEscalated), string(domain.StatusClosed))
		if err != nil {
			return fmt.Errorf("escalation: staged episodes query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var obligationPayload, watchPayload []byte
			if err := rows.Scan(&obligationPayload, &watchPayload); err != nil {
				return fmt.Errorf("escalation: staged episodes scan: %w", err)
			}
			var o domain.Obligation
			if err := json.Unmarshal(obligationPayload, &o); err != nil {
				return fmt.Errorf("escalation: decode obligation: %w", err)
			}
			var w domain.Watch
			if err := json.Unmarshal(watchPayload, &w); err != nil {
				return fmt.Errorf("escalation: decode watch: %w", err)
			}
			ep := Episode{Watch: w, Obligation: o}
			if s.configuredTarget != nil {
				ep.ConfiguredTarget = s.configuredTarget()
			}
			episodes = append(episodes, ep)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return episodes, nil
}

// appendEscalationLineOnce appends line unless a line for key is already in
// the file. That keeps ESCALATIONS.md at one row per episode.
func appendEscalationLineOnce(path, key, line string) error {
	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	needle := "episode=" + key
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), needle) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, err = f.WriteString(line)
	return err
}
