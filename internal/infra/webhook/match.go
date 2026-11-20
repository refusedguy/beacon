package webhook

import (
	"context"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// RoutingKey names one Watch. Every field takes part in the match: a delivery
// for another PR or for a head the Watch has already moved past must not
// refresh it (BS.27).
type RoutingKey struct {
	Repo    string
	PR      int
	HeadSHA string
}

// Valid reports whether the key carries everything the mapping needs.
func (k RoutingKey) Valid() bool {
	return k.Repo != "" && k.PR > 0 && k.HeadSHA != ""
}

// Event is the extracted, domain-shaped view of one signed delivery. The
// adapter fills these fields from the payload; the mapper never reads raw
// bytes and never looks at a status or conclusion.
type Event struct {
	Repo    string
	PR      int
	HeadSHA string
	Action  string
	At      time.Time
}

// Key trims the string fields and returns the routing key of the event.
func (e Event) Key() RoutingKey {
	return RoutingKey{
		Repo:    strings.TrimSpace(e.Repo),
		PR:      e.PR,
		HeadSHA: strings.TrimSpace(e.HeadSHA),
	}
}

// WatchSource supplies the Watches a delivery could refer to. An adapter may
// scan a store or narrow the set by repo and PR, but the mapper applies the
// full three-field key itself so a source cannot widen a match.
type WatchSource interface {
	CandidateWatches(ctx context.Context, key RoutingKey) ([]domain.Watch, error)
}

// Refresh is the intention to re-read CI for one Watch. It carries identifiers
// and the delivery action only. A status, conclusion or verdict has no field
// here: publishing a verdict stays with the CI bridge.
type Refresh struct {
	WatchID string
	Repo    string
	PR      int
	HeadSHA string
	Action  string
	At      time.Time
}

// RefreshIntent records the intention to refresh a Watch. The CI bridge owns
// the production implementation; this package declares only the port.
type RefreshIntent interface {
	RecordRefresh(ctx context.Context, refresh Refresh) error
}

// Outcome reason values. A no-match is an info event, never an error.
const (
	reasonMatched    = "matched"
	reasonNoMatch    = "no_watch_for_key"
	reasonIncomplete = "incomplete_routing_key"
)

// Outcome is the audit result of one mapping call.
type Outcome struct {
	Matched bool
	WatchID string
	Reason  string
}

// matchWatch is the pure core of the mapper. It returns the first Watch whose
// repo, PR and head SHA all equal the key. An empty list, a foreign PR or a
// stale head SHA all return Matched=false without touching any state.
func matchWatch(key RoutingKey, watches []domain.Watch) Outcome {
	for _, w := range watches {
		if w.Repo == key.Repo && w.PR == key.PR && w.HeadSHA == key.HeadSHA {
			return Outcome{Matched: true, WatchID: w.ID, Reason: reasonMatched}
		}
	}
	return Outcome{Reason: reasonNoMatch}
}

// MapEvent extracts the key from event, finds the matching Watch through
// source and records a refresh intent through intent. It never publishes a
// verdict and never changes a Watch. A missing match is logged at info level
// and returns a nil error. Errors from either port are returned unchanged.
func MapEvent(ctx context.Context, event Event, source WatchSource, intent RefreshIntent) (Outcome, error) {
	key := event.Key()
	if !key.Valid() {
		observe.Event(observe.LevelInfo, "webhook-watch-no-match", observe.Fields{
			"reason": reasonIncomplete, "repo": key.Repo, "pr": key.PR, "headSHA": key.HeadSHA,
		})
		return Outcome{Reason: reasonIncomplete}, nil
	}

	watches, err := source.CandidateWatches(ctx, key)
	if err != nil {
		return Outcome{}, err
	}
	outcome := matchWatch(key, watches)
	if !outcome.Matched {
		observe.Event(observe.LevelInfo, "webhook-watch-no-match", observe.Fields{
			"reason": outcome.Reason, "repo": key.Repo, "pr": key.PR, "headSHA": key.HeadSHA,
		})
		return outcome, nil
	}

	refresh := Refresh{
		WatchID: outcome.WatchID,
		Repo:    key.Repo,
		PR:      key.PR,
		HeadSHA: key.HeadSHA,
		Action:  strings.TrimSpace(event.Action),
		At:      event.At,
	}
	if err := intent.RecordRefresh(ctx, refresh); err != nil {
		return Outcome{}, err
	}
	return outcome, nil
}
