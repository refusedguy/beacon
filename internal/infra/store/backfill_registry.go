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
	"fmt"
	"strings"
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

// registryHasColumn reports whether the live agent_registry schema owns the
// named column. Migration 0005 has runtime, while a registry created on first
// use by registry.go does not. Both shapes are accepted.
func registryHasColumn(q columnQuerier, name string) (bool, error) {
	cols, err := tableColumns(q, "agent_registry")
	if err != nil {
		return false, fmt.Errorf("store: backfill: table info: %w", err)
	}
	return cols[strings.ToLower(name)], nil
}

// backfillInsertSQL builds the insert for the schema in use. The migration
// table needs runtime because it has no default; the registry-created table
// has no such column and rejects the wider insert.
func backfillInsertSQL(hasRuntime bool) string {
	if hasRuntime {
		return `INSERT OR IGNORE INTO agent_registry
		  (project_key, agent_id, role, runtime, session_id, notify_adapter,
		   notify_target, config_id, status, capabilities, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	return `INSERT OR IGNORE INTO agent_registry
		  (project_key, agent_id, role, session_id, notify_adapter,
		   notify_target, config_id, status, capabilities, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
}

// BackfillRegistry fills the project registry from the active watches. It is
// safe to call on every read: an existing executor row is never rewritten, so
// the same watch can be scanned any number of times. Subscribers with an empty
// ID are skipped because an executor needs an address to wake.
func (d *DB) BackfillRegistry(projectKey string, liveSessions []string) (BackfillResult, error) {
	if strings.TrimSpace(projectKey) == "" {
		return BackfillResult{}, domain.NewAppError(domain.CodeInvalidInput,
			"store: backfill: empty project key")
	}
	watches, err := d.ActiveWatches()
	if err != nil {
		return BackfillResult{}, fmt.Errorf("store: backfill: active watches: %w", err)
	}
	live := make(map[string]struct{}, len(liveSessions))
	for _, session := range liveSessions {
		if session != "" {
			live[session] = struct{}{}
		}
	}

	now := time.Now().UTC().UnixNano()
	var created, kept, stale int
	err = d.Write(context.Background(), func(tx *sql.Tx) error {
		if err := ensureRegistryTables(tx); err != nil {
			return err
		}
		hasRuntime, err := registryHasColumn(tx, "runtime")
		if err != nil {
			return err
		}
		insert := backfillInsertSQL(hasRuntime)
		for _, w := range watches {
			for _, s := range w.Subscribers {
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
				args := []any{projectKey, agentID, string(role)}
				if hasRuntime {
					args = append(args, backfillRuntime(role))
				}
				args = append(args, agentID, s.Adapter, s.Target, "", status,
					encodeCapabilities(nil), now, now)
				res, err := tx.Exec(insert, args...)
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
	executors, err := d.Executors(projectKey)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("store: backfill: read registry: %w", err)
	}
	return BackfillResult{
		WatchesScanned:   len(watches),
		ExecutorsCreated: created,
		ExecutorsKept:    kept,
		ExecutorsStale:   stale,
		Executors:        executors,
	}, nil
}
