// Package store: backfill of pre-migration Watch subscribers into the registry.
//
// Watches written before migration 0005 have no executor row. Backfill reads
// the active watches and creates one row per subscriber on first read, using
// Subscriber.Role and Subscriber.Adapter (ADR 0095, T5.26). A repeat leaves
// existing rows alone, so a later registry sync can lift stale without being
// undone by the next restart.
//
// The live session list and the registry belong to the same project (ADR 0086).
// A subscriber whose session is not in that list is unknown and gets stale.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	backfillRuntimeSubagent = "subagent"
	backfillRuntimePrimary  = "primary"
)

// BackfillResult reports one pass over the active watches. WatchesScanned
// counts watches, the other counters count subscribers. Executors is the
// registry of the project after the pass, including rows written earlier.
type BackfillResult struct {
	WatchesScanned   int
	ExecutorsCreated int
	ExecutorsKept    int
	ExecutorsStale   int
	Executors        []ExecutorRecord
}

// backfillRuntime maps a subscriber role to the runtime stored in the registry.
// Watch subscribers wait in a subagent session; an orchestrator runs in the
// primary session.
func backfillRuntime(role domain.Role) string {
	if role == domain.RoleOrchestrator {
		return backfillRuntimePrimary
	}
	return backfillRuntimeSubagent
}

// registryColumns caches the agent_registry column set for one write
// transaction. ensureRegistryTables probes four columns in the same
// transaction, so PRAGMA table_info runs once per write instead of once per
// probe (BCX-33). A *sql.DB querier is never cached: its callers verify schema
// changes and must see the live table.
var (
	registryColumnsMu   sync.Mutex
	registryColumnsTx   *sql.Tx
	registryColumnsData map[string]bool
)

func registryColumns(q columnQuerier) (map[string]bool, error) {
	tx, inTx := q.(*sql.Tx)
	if !inTx {
		return tableColumns(q, "agent_registry")
	}
	registryColumnsMu.Lock()
	defer registryColumnsMu.Unlock()
	if registryColumnsTx == tx && registryColumnsData != nil {
		return registryColumnsData, nil
	}
	cols, err := tableColumns(q, "agent_registry")
	if err != nil {
		return nil, err
	}
	registryColumnsTx = tx
	registryColumnsData = cols
	return cols, nil
}

// registryHasColumn reports whether the live agent_registry schema owns the
// named column. EnsureRegistrySchema adds the columns read paths use, so a
// caller can check a table that an older build created (ARDH-02). Inside one
// write transaction the column set is read once and reused by every probe.
func registryHasColumn(q columnQuerier, name string) (bool, error) {
	cols, err := registryColumns(q)
	if err != nil {
		return false, fmt.Errorf("store: backfill: table info: %w", err)
	}
	return cols[strings.ToLower(name)], nil
}

// backfillInsertSQL is the single insert shape. The canonical schema from
// migration 0005 always has runtime, and EnsureRegistrySchema adds it to a
// table created by an older build, so no per-schema variant is needed.
const backfillInsertSQL = `INSERT OR IGNORE INTO agent_registry
		  (project_key, agent_id, role, runtime, session_id, notify_adapter,
		   notify_target, config_id, status, capabilities, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// backfillBatchSize bounds one page of active watches. A cursor over id keeps
// the read bounded and lets a large registry be written in short transactions
// instead of one that grows with the whole active set.
const backfillBatchSize = 200

type backfillSubscribers struct {
	id          string
	subscribers []domain.Subscriber
}

// activeSubscriberPage reads one page of active watches and returns only their
// subscriber lists. Backfill needs subscribers, not the whole Watch, so the
// query extracts that field and avoids decoding checks, merge, flaky and
// policy payloads on every call. The id cursor makes the scan resumable.
func (d *DB) activeSubscriberPage(cursor string, limit int) ([]backfillSubscribers, error) {
	rows, err := d.readDB.Query(`
		SELECT id, json_extract(payload, '$.subscribers')
		FROM watches
		WHERE status IN (?, ?) AND id > ?
		ORDER BY id LIMIT ?`,
		string(domain.StatusWatching), string(domain.StatusNotifying), cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []backfillSubscribers
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var subs []domain.Subscriber
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &subs); err != nil {
				return nil, fmt.Errorf("store: backfill: subscribers of %s: %w", id, err)
			}
		}
		out = append(out, backfillSubscribers{id: id, subscribers: subs})
	}
	return out, rows.Err()
}

// BackfillRegistry fills the project registry from the active watches. It is
// safe to call on every read: an existing executor row is never rewritten, so
// the same watch can be scanned any number of times. Subscribers with an empty
// ID are skipped because an executor needs an address to wake. Watches are read
// in cursor-paged batches so neither the decode work nor one writer transaction
// grows with the whole active set.
func (d *DB) BackfillRegistry(projectKey string, liveSessions []string) (BackfillResult, error) {
	if strings.TrimSpace(projectKey) == "" {
		return BackfillResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: backfill: empty project key")
	}
	live := make(map[string]struct{}, len(liveSessions))
	for _, session := range liveSessions {
		if session != "" {
			live[session] = struct{}{}
		}
	}

	now := time.Now().UTC().UnixNano()
	var created, kept, stale, scanned int
	cursor := ""
	for {
		page, err := d.activeSubscriberPage(cursor, backfillBatchSize)
		if err != nil {
			return BackfillResult{}, fmt.Errorf("store: backfill: active watches: %w", err)
		}
		if len(page) == 0 {
			break
		}
		scanned += len(page)
		err = d.Write(context.Background(), func(tx *sql.Tx) error {
			if err := ensureRegistryTables(tx); err != nil {
				return err
			}
			for _, ws := range page {
				for _, s := range ws.subscribers {
					agentID := strings.TrimSpace(s.ID)
					if agentID == "" {
						continue
					}
					role := s.Role
					if role == "" {
						role = domain.RoleExecutor
					}
					status := RegistryStatusStale
					if _, ok := live[agentID]; ok {
						status = RegistryStatusActive
					}
					args := []any{projectKey, agentID, string(role), backfillRuntime(role),
						agentID, s.Adapter, s.Target, "", status,
						encodeCapabilities(nil), now, now}
					res, err := tx.Exec(backfillInsertSQL, args...)
					if err != nil {
						return fmt.Errorf("store: backfill: insert %s: %w", agentID, err)
					}
					n, err := res.RowsAffected()
					if err != nil {
						return fmt.Errorf("store: backfill: rows affected: %w", err)
					}
					if n == 1 {
						created++
						if status == RegistryStatusStale {
							stale++
						}
					} else {
						kept++
					}
				}
			}
			return nil
		})
		if err != nil {
			return BackfillResult{}, err
		}
		cursor = page[len(page)-1].id
		if len(page) < backfillBatchSize {
			break
		}
	}
	executors, err := d.Executors(projectKey)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("store: backfill: read registry: %w", err)
	}
	return BackfillResult{
		WatchesScanned:   scanned,
		ExecutorsCreated: created,
		ExecutorsKept:    kept,
		ExecutorsStale:   stale,
		Executors:        executors,
	}, nil
}
