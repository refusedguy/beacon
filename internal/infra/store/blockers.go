// Package store is the SQLite implementation of the domain state ports.
//
// This file owns the control-plane tables added by migration 0006: blocker
// incidents, project orchestrator bindings and runtime stats. All writes go
// through DB.Write so an incident and the Watches it unblocks change in one
// transaction.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// Incident status lives in its own column. Local literals keep this file
	// independent of the status names chosen by the domain card.
	blockerStatusOpen     = "open"
	blockerStatusResolved = "resolved"

	defaultMaxFixAttempts = 3
	runtimeStatsRowID     = 1
)

// ensureControlPlaneTables is the on-first-use guard. Open applies the
// migration, but replaying its idempotent body keeps these methods safe when a
// state file was touched by an older binary between checks.
func ensureControlPlaneTables(tx *sql.Tx) error {
	body, err := migrationFS.ReadFile("migrations/0006_control_plane.sql")
	if err != nil {
		return fmt.Errorf("store: control plane: read migration: %w", err)
	}
	if _, err := tx.Exec(string(body)); err != nil {
		return fmt.Errorf("store: control plane: ensure tables: %w", err)
	}
	return nil
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func timeFromUnixNano(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// sortedChecks returns a sorted copy so the caller's slice is never mutated.
func sortedChecks(checks []string) []string {
	out := append([]string(nil), checks...)
	sort.Strings(out)
	return out
}

func blockerSignature(failedChecks []string) string {
	sum := sha256.Sum256([]byte(strings.Join(sortedChecks(failedChecks), "\n")))
	return hex.EncodeToString(sum[:])
}

func blockerID(repo, baseRef, signature string) string {
	sum := sha256.Sum256([]byte(strings.Join(
		[]string{repo, baseRef, signature}, "\x00")))
	return "blk_" + hex.EncodeToString(sum[:])
}

func loadBlocker(tx *sql.Tx, id string) (domain.BlockerIncident, error) {
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM blocker_incidents WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BlockerIncident{}, fmt.Errorf("store: blocker %s: %w", id, err)
	}
	if err != nil {
		return domain.BlockerIncident{}, err
	}
	var b domain.BlockerIncident
	if err := json.Unmarshal(payload, &b); err != nil {
		return domain.BlockerIncident{}, fmt.Errorf("store: blocker %s: decode: %w", id, err)
	}
	return b, nil
}

func saveBlocker(tx *sql.Tx, b domain.BlockerIncident) error {
	payload, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("store: blocker %s: encode: %w", b.ID, err)
	}
	_, err = tx.Exec(`
		INSERT INTO blocker_incidents
		  (id, repo, base_ref, signature, status, fixer_watch_id,
		   fix_attempts, max_fix_attempts, payload, created_at, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  repo             = excluded.repo,
		  base_ref         = excluded.base_ref,
		  signature        = excluded.signature,
		  status           = excluded.status,
		  fixer_watch_id   = excluded.fixer_watch_id,
		  fix_attempts     = excluded.fix_attempts,
		  max_fix_attempts = excluded.max_fix_attempts,
		  payload          = excluded.payload,
		  created_at       = excluded.created_at,
		  resolved_at      = excluded.resolved_at`,
		b.ID, b.Repo, b.BaseRef, b.Signature, b.Status, b.FixerWatchID,
		b.FixAttempts, b.MaxFixAttempts, payload,
		unixNano(b.CreatedAt), unixNano(b.ResolvedAt))
	return err
}

func loadWatchTx(tx *sql.Tx, id string) (domain.Watch, error) {
	var payload []byte
	if err := tx.QueryRow(`SELECT payload FROM watches WHERE id = ?`, id).Scan(&payload); err != nil {
		return domain.Watch{}, err
	}
	return decodeWatch(payload)
}

// saveWatchTx is the transaction-local twin of SaveWatch. It lets a blocker
// resolution rewrite Watches in the same transaction that resolves the
// incident. SaveWatch itself delegates here.
func saveWatchTx(tx *sql.Tx, w domain.Watch) error {
	payload, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("store: watch %s: encode: %w", w.ID, err)
	}
	_, err = tx.Exec(`
		INSERT INTO watches (id, repo, pr, head_sha, status, revision, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  repo     = excluded.repo,
		  pr       = excluded.pr,
		  head_sha = excluded.head_sha,
		  status   = excluded.status,
		  revision = excluded.revision,
		  payload  = excluded.payload`,
		w.ID, w.Repo, w.PR, w.HeadSHA, string(w.Status), w.Revision, payload)
	return err
}

// GetOrCreateOpenBlocker folds failed checks into a durable incident. The
// signature comes from sorted check names, so check order in a CI payload does
// not create a second incident. A repeated registration of the same Watch adds
// no duplicate to BlockedWatchIDs.
func (d *DB) GetOrCreateOpenBlocker(repo, baseRef string, failedChecks []string, watchID string, now time.Time) (domain.BlockerIncident, bool, error) {
	signature := blockerSignature(failedChecks)
	var out domain.BlockerIncident
	created := false

	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureControlPlaneTables(tx); err != nil {
			return err
		}
		var payload []byte
		err := tx.QueryRow(`
			SELECT payload FROM blocker_incidents
			WHERE repo = ? AND base_ref = ? AND signature = ? AND status = ?
			ORDER BY created_at LIMIT 1`,
			repo, baseRef, signature, blockerStatusOpen).Scan(&payload)
		if err == nil {
			if err := json.Unmarshal(payload, &out); err != nil {
				return fmt.Errorf("store: blocker: decode: %w", err)
			}
			for _, id := range out.BlockedWatchIDs {
				if id == watchID {
					return nil
				}
			}
			out.BlockedWatchIDs = append(out.BlockedWatchIDs, watchID)
			return saveBlocker(tx, out)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		out = domain.BlockerIncident{
			ID:              blockerID(repo, baseRef, signature),
			Repo:            repo,
			BaseRef:         baseRef,
			Signature:       signature,
			Status:          blockerStatusOpen,
			BlockedWatchIDs: []string{watchID},
			FixAttempts:     0,
			MaxFixAttempts:  defaultMaxFixAttempts,
			FailedChecks:    sortedChecks(failedChecks),
			CreatedAt:       now.UTC(),
		}
		created = true
		return saveBlocker(tx, out)
	})
	if err != nil {
		return domain.BlockerIncident{}, false, err
	}
	return out, created, nil
}

// AssignFixer records the Watch that will repair an incident. This is the only
// place that increments FixAttempts, so a rebind or a read cannot spend the
// fixer budget by accident.
func (d *DB) AssignFixer(blockerID, fixerWatchID string) (domain.BlockerIncident, error) {
	var out domain.BlockerIncident
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureControlPlaneTables(tx); err != nil {
			return err
		}
		b, err := loadBlocker(tx, blockerID)
		if err != nil {
			return err
		}
		b.FixerWatchID = fixerWatchID
		b.FixAttempts++
		if err := saveBlocker(tx, b); err != nil {
			return err
		}
		out = b
		return nil
	})
	return out, err
}

// resolveBlockerTx resolves one incident and unblocks every Watch attached to
// it. Already-resolved incidents and Watches that left blocked state are
// skipped, which makes the operation idempotent under redelivery.
func resolveBlockerTx(tx *sql.Tx, blockerID string, now time.Time) ([]domain.Watch, error) {
	b, err := loadBlocker(tx, blockerID)
	if err != nil {
		return nil, err
	}
	if b.Status == blockerStatusResolved {
		return nil, nil
	}
	b.Status = blockerStatusResolved
	b.ResolvedAt = now.UTC()
	if err := saveBlocker(tx, b); err != nil {
		return nil, err
	}

	var unblocked []domain.Watch
	for _, watchID := range b.BlockedWatchIDs {
		w, err := loadWatchTx(tx, watchID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if w.Status != domain.StatusBlocked {
			continue
		}
		w.Status = domain.StatusWatching
		w.Verdict = domain.VerdictWatching
		w.Blocked = nil
		w.BlockerID = ""
		w.Revision++
		if err := saveWatchTx(tx, w); err != nil {
			return nil, err
		}
		unblocked = append(unblocked, w)
	}
	return unblocked, nil
}

// ResolveBlocker resolves an incident and returns the Watches it unblocked.
func (d *DB) ResolveBlocker(blockerID string, now time.Time) ([]domain.Watch, error) {
	var out []domain.Watch
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureControlPlaneTables(tx); err != nil {
			return err
		}
		watches, err := resolveBlockerTx(tx, blockerID, now)
		out = watches
		return err
	})
	return out, err
}

// ResolveBlockersByBaseGreen resolves the open incidents of repo/baseRef whose
// failed checks are all green on base: a check absent from baseFailed no longer
// blocks the branch. The incident transition and every Watch unblock happen in
// one transaction. It returns the incidents that were resolved.
func (d *DB) ResolveBlockersByBaseGreen(repo, baseRef string, baseFailed map[string]bool, now time.Time) ([]domain.BlockerIncident, error) {
	var resolved []domain.BlockerIncident
	err := d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureControlPlaneTables(tx); err != nil {
			return err
		}
		rows, err := tx.Query(`
			SELECT id, payload FROM blocker_incidents
			WHERE repo = ? AND base_ref = ? AND status = ? ORDER BY created_at`,
			repo, baseRef, blockerStatusOpen)
		if err != nil {
			return err
		}
		type candidate struct {
			id      string
			payload []byte
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.payload); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		for _, c := range candidates {
			var b domain.BlockerIncident
			if err := json.Unmarshal(c.payload, &b); err != nil {
				return fmt.Errorf("store: blocker %s: decode: %w", c.id, err)
			}
			green := true
			for _, check := range b.FailedChecks {
				if baseFailed[check] {
					green = false
					break
				}
			}
			if !green {
				continue
			}
			if _, err := resolveBlockerTx(tx, c.id, now); err != nil {
				return err
			}
			updated, err := loadBlocker(tx, c.id)
			if err != nil {
				return err
			}
			resolved = append(resolved, updated)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// ActiveBlockers returns every open incident, oldest first.
func (d *DB) ActiveBlockers() ([]domain.BlockerIncident, error) {
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "blocker_incidents")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := d.readDB.Query(`
		SELECT payload FROM blocker_incidents WHERE status = ? ORDER BY created_at`,
		blockerStatusOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.BlockerIncident
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var b domain.BlockerIncident
		if err := json.Unmarshal(payload, &b); err != nil {
			return nil, fmt.Errorf("store: blocker: decode: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BindOrchestrator records the session currently driving a project. Binding
// again with the same key replaces the session, adapter and timestamp.
func (d *DB) BindOrchestrator(projectKey, sessionID, adapter string, now time.Time) error {
	if strings.TrimSpace(projectKey) == "" || strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("store: bind orchestrator: project key and session id must be non-empty")
	}
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureControlPlaneTables(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO project_orchestrators (project_key, session_id, adapter, bound_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(project_key) DO UPDATE SET
			  session_id = excluded.session_id,
			  adapter    = excluded.adapter,
			  bound_at   = excluded.bound_at`,
			projectKey, sessionID, adapter, unixNano(now))
		return err
	})
}

// BoundOrchestrator loads the session bound to a project. The boolean is false
// when no binding exists.
func (d *DB) BoundOrchestrator(projectKey string) (domain.OrchestratorBinding, bool, error) {
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "project_orchestrators")
	if err != nil || !ok {
		return domain.OrchestratorBinding{}, false, err
	}
	var (
		binding domain.OrchestratorBinding
		boundAt int64
	)
	err = d.readDB.QueryRow(`
		SELECT project_key, session_id, adapter, bound_at
		FROM project_orchestrators WHERE project_key = ?`, projectKey,
	).Scan(&binding.ProjectKey, &binding.SessionID, &binding.Adapter, &boundAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.OrchestratorBinding{}, false, nil
	}
	if err != nil {
		return domain.OrchestratorBinding{}, false, err
	}
	binding.BoundAt = timeFromUnixNano(boundAt)
	return binding, true, nil
}

// SaveRuntimeStats replaces the single runtime-stats row.
func (d *DB) SaveRuntimeStats(payload []byte, now time.Time) error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureControlPlaneTables(tx); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO runtime_stats (id, payload, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
			  payload    = excluded.payload,
			  updated_at = excluded.updated_at`,
			runtimeStatsRowID, payload, unixNano(now))
		return err
	})
}

// LoadRuntimeStats returns the stored payload. The boolean is false when no
// snapshot has been saved yet.
func (d *DB) LoadRuntimeStats() ([]byte, bool, error) {
	ctx := context.Background()
	ok, err := tableExists(ctx, d.readDB, "runtime_stats")
	if err != nil || !ok {
		return nil, false, err
	}
	var payload []byte
	err = d.readDB.QueryRow(`SELECT payload FROM runtime_stats WHERE id = ?`,
		runtimeStatsRowID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return payload, true, nil
}

// MarkObligationDelivered persists a delivery in the obligations row itself,
// so a process restart cannot forget it. The attempt number is recorded once;
// lastErr is kept in the payload as opaque diagnostics and never turns a
// delivered fact back into a pending one.
func (d *DB) MarkObligationDelivered(id string, at time.Time, attempt int, lastErr string) error {
	return d.Write(context.Background(), func(tx *sql.Tx) error {
		var payload []byte
		err := tx.QueryRow(`SELECT payload FROM obligations WHERE id = ?`, id).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: obligation %s: %w", id, err)
		}
		if err != nil {
			return err
		}
		var o domain.Obligation
		if err := json.Unmarshal(payload, &o); err != nil {
			return fmt.Errorf("store: obligation %s: decode: %w", id, err)
		}
		if o.DeliveredAt.IsZero() {
			o.DeliveredAt = at
		}
		if attempt > 0 {
			seen := false
			for _, a := range o.Attempts {
				if a.N == attempt {
					seen = true
					break
				}
			}
			if !seen {
				o.Attempts = append(o.Attempts, domain.Attempt{
					N: attempt, At: at, Outcome: domain.OutcomePosted,
				})
			}
		}
		updated, err := json.Marshal(o)
		if err != nil {
			return fmt.Errorf("store: obligation %s: encode: %w", id, err)
		}
		if lastErr != "" {
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(updated, &doc); err != nil {
				return err
			}
			raw, err := json.Marshal(lastErr)
			if err != nil {
				return err
			}
			doc["lastDeliveryError"] = raw
			updated, err = json.Marshal(doc)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE obligations SET delivered_at = ?, payload = ? WHERE id = ?`,
			1, updated, id)
		return err
	})
}

// ResetControlPlaneState clears the control-plane rows for `beacon reset
// --yes`. Tables created on first use may be absent, so each delete is guarded
// by an existence check. Meta, receipts and the agent registry are preserved.
func (d *DB) ResetControlPlaneState(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{
			"watches",
			"obligations",
			"blocker_incidents",
			"events",
			"orchestrator_merges",
			"runtime_stats",
		} {
			ok, err := tableExistsTx(tx, table)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return fmt.Errorf("store: reset: delete %s: %w", table, err)
			}
		}
		return nil
	})
}

func tableExistsTx(tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&n)
	return n == 1, err
}
