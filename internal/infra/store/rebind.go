// Package store is the SQLite implementation of the domain state ports.
//
// This file owns the in-place orchestrator rebind. It stays in the store so the
// CLI can ask for the operation with plain strings and plain result data,
// without importing domain types across the ADR 0005 layer boundary.
package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// rebindReasonField and rebindReasonSubscriber name what was replaced on a
	// Watch. The summary wake carries them next to watchID and PR.
	rebindReasonField      = "orchestratorID"
	rebindReasonSubscriber = "orchestrator subscriber"
)

// RebindTarget is one active Watch touched by a rebind. It is plain data so a
// caller outside the store can render a wake without naming domain.Watch.
type RebindTarget struct {
	WatchID string `json:"watchID"`
	PR      int    `json:"pr"`
	Reason  string `json:"reason"`
}

// RebindOrchestrator replaces every occurrence of from with to in the
// orchestrator identity of live Watches, including blocked ones: the Watch.OrchestratorID field and,
// when the old session was the routing subscriber, that subscriber's id and
// target. Watches are saved under the same id, so no Watch is created or
// closed. With dryRun set, the same targets are reported and nothing is
// written.
//
// The OrchestratorID field is stored data only. Updating the orchestrator
// subscriber is what moves future blocked wakes to the new session; the field
// itself is never read to choose a wake recipient.
func (d *DB) RebindOrchestrator(from, to string, dryRun bool) ([]RebindTarget, error) {
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || from == to {
		return nil, fmt.Errorf("store: rebind: from and to must be non-empty and different")
	}
	// Blocked Watches still carry the orchestrator identity and must follow
	// the new session too. ActiveWatches deliberately omits them.
	watches, err := d.LiveWatches()
	if err != nil {
		return nil, err
	}

	var out []RebindTarget
	for i := range watches {
		w := watches[i]
		byField := w.OrchestratorID == from
		bySubscriber := hasOrchestratorSubscriber(w, from)
		if !byField && !bySubscriber {
			continue
		}

		reasons := make([]string, 0, 2)
		if byField {
			reasons = append(reasons, rebindReasonField)
		}
		if bySubscriber {
			reasons = append(reasons, rebindReasonSubscriber)
		}
		out = append(out, RebindTarget{
			WatchID: w.ID,
			PR:      w.PR,
			Reason:  strings.Join(reasons, " + "),
		})
		if dryRun {
			continue
		}

		if byField {
			w.OrchestratorID = to
		}
		for j := range w.Subscribers {
			sub := &w.Subscribers[j]
			if sub.OrchestratorID == from {
				sub.OrchestratorID = to
			}
			if sub.Role == domain.RoleOrchestrator && sub.ID == from {
				sub.ID = to
				if sub.Target == from {
					sub.Target = to
				}
			}
		}
		if err := d.SaveWatch(w); err != nil {
			return nil, fmt.Errorf("store: rebind watch %s: %w", w.ID, err)
		}
	}
	return out, nil
}

// BindOrchestratorAndRebind moves every live Watch off the session currently
// bound to projectKey, then points the project binding at to. The adapter is
// preserved. The binding changes only after the rebind succeeds, so a failure
// leaves both the Watches and the binding on the old session.
func (d *DB) BindOrchestratorAndRebind(projectKey, to string) ([]RebindTarget, error) {
	projectKey = strings.TrimSpace(projectKey)
	to = strings.TrimSpace(to)
	if projectKey == "" || to == "" {
		return nil, fmt.Errorf("store: rebind: project key and to must be non-empty")
	}
	binding, bound, err := d.BoundOrchestrator(projectKey)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, fmt.Errorf("store: rebind: no orchestrator bound for project %s", projectKey)
	}
	if binding.SessionID == to {
		return nil, nil
	}

	targets, err := d.RebindOrchestrator(binding.SessionID, to, false)
	if err != nil {
		return nil, err
	}
	if err := d.BindOrchestrator(projectKey, to, binding.Adapter, time.Now().UTC()); err != nil {
		return nil, err
	}
	return targets, nil
}

// hasOrchestratorSubscriber reports whether from is the id of the subscriber
// that currently receives blocked wakes for w.
func hasOrchestratorSubscriber(w domain.Watch, from string) bool {
	for _, sub := range w.Subscribers {
		if sub.Role == domain.RoleOrchestrator && sub.ID == from {
			return true
		}
	}
	return false
}
