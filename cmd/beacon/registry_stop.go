package main

// T5.51: checked stop of a registry executor (ADR 0091).
//
// A stop is safe only while the registry still holds the selection the operator
// decided to stop. This file reads that selection through a narrow infra port,
// compares it with the expected values, and checks that the session has no
// undelivered obligations. It never kills anything: the caller runs the check
// before the terminal effect (ADR 0091, eval r05).
//
// T5.52 owns the uncertain-stop outcome and T5.56 owns the CLI registration.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/store"
)

// Refusal reasons are stable strings so callers and the CLI can branch on them
// without parsing the human-readable Detail.
const (
	stopRefusalSelectionChanged  = "selection_changed"
	stopRefusalActiveObligations = "active_obligations"
)

// registryStopSelection is the four-value precondition of a checked stop: the
// Watch id and revision, the executor session and the fencing token that guard
// the binding. All four must match before a stop may proceed (ADR 0091).
type registryStopSelection struct {
	WatchID   string
	Revision  uint64
	SessionID string
	Token     string
}

// registryStopStore is the read-only infra port of a checked stop. It reads the
// registry selection from SQLite and counts the session's undelivered
// obligations. It has no kill and no write method, so running the check cannot
// stop an executor or change the registry (ADR 0091, ADR 0090).
type registryStopStore interface {
	// Selection returns the current registry binding for watchID. found is
	// false when the registry holds no binding for that watch.
	Selection(watchID string) (registryStopSelection, bool, error)
	// ActiveObligations returns the number of undelivered obligations held by
	// the session.
	ActiveObligations(sessionID string) (int, error)
}

// registryStopVerdict is the result of the precondition check. Reason is empty
// when Allowed is true. A refusal carries no stop phase: the stop has not
// started, so there is no phase to report (eval r05).
type registryStopVerdict struct {
	Allowed bool
	Reason  string
	Detail  string
}

// selectionDiff lists the fields that differ between the stored selection and
// the expected one. The list gives the operator a concrete reason to refresh
// the precondition instead of a bare mismatch.
func selectionDiff(current, want registryStopSelection) []string {
	var diff []string
	if current.WatchID != want.WatchID {
		diff = append(diff, fmt.Sprintf("watchID: want %q, got %q", want.WatchID, current.WatchID))
	}
	if current.Revision != want.Revision {
		diff = append(diff, fmt.Sprintf("revision: want %d, got %d", want.Revision, current.Revision))
	}
	if current.SessionID != want.SessionID {
		diff = append(diff, fmt.Sprintf("sessionID: want %q, got %q", want.SessionID, current.SessionID))
	}
	if current.Token != want.Token {
		diff = append(diff, fmt.Sprintf("token: want %q, got %q", want.Token, current.Token))
	}
	return diff
}

func validateStopSelection(want registryStopSelection) error {
	switch {
	case strings.TrimSpace(want.WatchID) == "":
		return fmt.Errorf("registry stop: expected watchID is required")
	case want.Revision == 0:
		return fmt.Errorf("registry stop: expected revision is required")
	case strings.TrimSpace(want.SessionID) == "":
		return fmt.Errorf("registry stop: expected sessionID is required")
	case strings.TrimSpace(want.Token) == "":
		return fmt.Errorf("registry stop: expected token is required")
	}
	return nil
}

// checkRegistryStop runs the precondition of a checked stop. It reads the
// registry selection through the port and refuses with selection_changed when
// any of the four values differs from the expected ones. If the selection still
// matches, it refuses with active_obligations while the session holds work that
// has not been delivered. Only a matching selection with no active obligations
// allows the caller to proceed to the stop.
func checkRegistryStop(port registryStopStore, want registryStopSelection) (registryStopVerdict, error) {
	if port == nil {
		return registryStopVerdict{}, fmt.Errorf("registry stop: store not connected")
	}
	if err := validateStopSelection(want); err != nil {
		return registryStopVerdict{}, err
	}

	current, found, err := port.Selection(want.WatchID)
	if err != nil {
		return registryStopVerdict{}, fmt.Errorf("registry stop: read selection: %w", err)
	}
	if !found {
		return registryStopVerdict{
			Reason: stopRefusalSelectionChanged,
			Detail: fmt.Sprintf("registry has no binding for watch %q", want.WatchID),
		}, nil
	}
	if diff := selectionDiff(current, want); len(diff) > 0 {
		return registryStopVerdict{
			Reason: stopRefusalSelectionChanged,
			Detail: "selection changed: " + strings.Join(diff, "; "),
		}, nil
	}

	active, err := port.ActiveObligations(want.SessionID)
	if err != nil {
		return registryStopVerdict{}, fmt.Errorf("registry stop: read obligations: %w", err)
	}
	if active > 0 {
		return registryStopVerdict{
			Reason: stopRefusalActiveObligations,
			Detail: fmt.Sprintf("session %q holds %d undelivered obligation(s)", want.SessionID, active),
		}, nil
	}

	return registryStopVerdict{Allowed: true}, nil
}

// registryStopDBStore adapts the existing store reads to registryStopStore.
// Selection uses the latest binding of the watch, so a new revision makes the
// precondition fail. ActiveObligations uses the pending-obligation view, which
// contains only rows with no delivered_at timestamp.
type registryStopDBStore struct {
	db *store.DB
}

func (s registryStopDBStore) Selection(watchID string) (registryStopSelection, bool, error) {
	if s.db == nil {
		return registryStopSelection{}, false, fmt.Errorf("registry stop: database not connected")
	}
	binding, found, err := s.db.LatestWatchBinding(watchID)
	if err != nil || !found {
		return registryStopSelection{}, found, err
	}
	return registryStopSelection{
		WatchID:   binding.WatchID,
		Revision:  binding.Revision,
		SessionID: binding.SubscriberID,
		Token:     strconv.FormatInt(binding.FencingToken, 10),
	}, true, nil
}

func (s registryStopDBStore) ActiveObligations(sessionID string) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("registry stop: database not connected")
	}
	pending, err := s.db.PendingObligations()
	if err != nil {
		return 0, err
	}
	active := 0
	for _, obligation := range pending {
		if obligation.SubscriberID == sessionID {
			active++
		}
	}
	return active, nil
}
