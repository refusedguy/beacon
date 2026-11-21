// Package domain holds the Beacon entities, status enums and port interfaces.
// This file adds watch registration and subscriber fan-out on top of those
// types. It uses only the standard library and the ports declared in types.go.
package domain

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxWatchesPerSubscriber is the per-subscriber active Watch budget from
	// WORKER-PROMPT §4.2 ("50 Watch на подписчика").
	maxWatchesPerSubscriber = 50

	// maxActiveWatches is the global active Watch budget from the same section.
	maxActiveWatches = 200

	// maxSubscribersPerWatch caps a single Watch's fan-out at the same number
	// as the per-subscriber budget. T1.4's check exercises the 51st subscriber
	// on one PR and expects LIMIT_EXCEEDED, so the cap is enforced here too.
	maxSubscribersPerWatch = 50

	// WatchHandleTTL is how long a watchID handle stays usable after issue.
	// A Watch may keep running past this point (ADR 0019); only the handle goes
	// stale. A caller holding a stale handle registers again to get a fresh one.
	WatchHandleTTL = 30 * time.Minute
)

// Subscription is the result of registering a subscriber on a Watch. Watch is
// the (possibly reused) watch with the subscriber applied. Obligation is the
// subscriber's own claim; Renewed reports that the subscriber already existed
// and no new obligation was created for it.
type Subscription struct {
	Watch      Watch
	Obligation Obligation
	Renewed    bool
}

// Fanout applies the registration and fan-out rules. It holds a Store and a
// Clock through their domain ports. The mutex serializes the check-then-act
// sequence so the alias and limit invariants hold under concurrent calls in
// one process. A single writer per Store is still assumed; cross-process
// exclusion belongs to the daemon's lock (ADR 0021).
type Fanout struct {
	mu        sync.Mutex
	store     Store
	clock     Clock
	streamSeq uint64
}

// NewFanout builds a Fanout over the given ports. A nil clock falls back to
// the wall clock. A nil store is rejected when Subscribe is called.
func NewFanout(store Store, clock Clock) *Fanout {
	if clock == nil {
		clock = wallClock{}
	}
	return &Fanout{store: store, clock: clock}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// EmitStreamEvent assigns the next monotonic seq to one chunk for watchID and
// returns it. Seq starts at one and increases across every call on one Fanout,
// so a client can restore order over a whole stream, spot a gap as missing and
// spot a repeated chunk as a duplicate (ADR 0031 §1). The same mutex that
// serializes subscription check-then-act keeps numbering correct under
// concurrent fan-out.
func (f *Fanout) EmitStreamEvent(watchID string, payload []byte) StreamEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamSeq++
	return StreamEvent{Seq: f.streamSeq, WatchID: watchID, Payload: payload}
}

// Subscribe registers s on the Watch identified by w's (repo, pr, headSHA)
// alias. Reusing the alias keeps at most one active Watch per PR head. An
// existing subscriber renews and keeps its obligation; a new subscriber is
// appended and gets its own obligation keyed by T1.3's ObligationKey. Budgets
// over 50 watches per subscriber, 50 subscribers per watch and 200 active
// watches are rejected with LIMIT_EXCEEDED.
func (f *Fanout) Subscribe(w Watch, s Subscriber) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.store == nil {
		return Subscription{}, NewAppError(CodePersistFailed, "fanout: store is nil")
	}
	if s.ID == "" {
		return Subscription{}, NewAppError(CodeInvalidInput, "fanout: subscriber id is empty")
	}

	now := f.clock.Now()
	active, err := f.store.ActiveWatches()
	if err != nil {
		return Subscription{}, NewAppError(CodePersistFailed, err.Error())
	}
	all, err := f.store.AllWatches()
	if err != nil {
		return Subscription{}, NewAppError(CodePersistFailed, err.Error())
	}

	existing := findByAlias(active, w.Repo, w.PR, w.HeadSHA)
	newWatch := existing == nil

	target := w
	if newWatch {
		if len(active) >= maxActiveWatches {
			return Subscription{}, NewAppError(CodeLimitExceeded, "fanout: global active watch limit reached")
		}
		target = normalizeWatch(w, all, now)
	} else {
		// Work on a copy of the stored watch so callers' values are not aliased.
		target = cloneWatch(*existing)
	}

	renew := hasSubscriber(target, s.ID)
	if !renew {
		if countSubscriberWatches(active, s.ID) >= maxWatchesPerSubscriber {
			return Subscription{}, NewAppError(CodeLimitExceeded, "fanout: per-subscriber watch limit reached")
		}
		if len(target.Subscribers) >= maxSubscribersPerWatch {
			return Subscription{}, NewAppError(CodeLimitExceeded, "fanout: per-watch subscriber limit reached")
		}
	}

	// Every subscriber on the watch owns a distinct obligation at this revision.
	for i := range target.Subscribers {
		if _, err := f.ensureObligation(target, target.Subscribers[i].ID, now); err != nil {
			return Subscription{}, err
		}
	}
	o, err := f.ensureObligation(target, s.ID, now)
	if err != nil {
		return Subscription{}, err
	}

	if renew {
		for i := range target.Subscribers {
			if target.Subscribers[i].ID == s.ID {
				target.Subscribers[i].LastActivity = now
				break
			}
		}
	} else {
		stamp := s
		stamp.LastActivity = now
		if stamp.AddedAt.IsZero() {
			stamp.AddedAt = now
		}
		target.Subscribers = append(target.Subscribers, stamp)
	}

	if err := f.store.SaveWatch(target); err != nil {
		return Subscription{}, NewAppError(CodePersistFailed, err.Error())
	}

	return Subscription{Watch: target, Obligation: o, Renewed: renew}, nil
}

// ensureObligation returns the subscriber's obligation for the watch's current
// revision, creating and persisting one only when it is missing.
func (f *Fanout) ensureObligation(w Watch, subscriberID string, now time.Time) (Obligation, error) {
	id := ObligationKey(w.ID, w.Revision, subscriberID)
	o, err := f.store.Obligation(id)
	if err == nil && o.ID == id {
		return o, nil
	}

	o = Obligation{
		ID:             id,
		WatchID:        w.ID,
		Revision:       w.Revision,
		SubscriberID:   subscriberID,
		VerdictPointer: w.Verdict,
		StagedAt:       now,
	}
	if err := f.store.SaveObligation(o); err != nil {
		return Obligation{}, NewAppError(CodePersistFailed, err.Error())
	}
	return o, nil
}

// findByAlias returns the active Watch with the same (repo, pr, headSHA) key.
// Closed watches never take part in the ≤1 active invariant.
func findByAlias(watches []Watch, repo string, pr int, headSHA string) *Watch {
	for i := range watches {
		w := &watches[i]
		if w.Status == StatusClosed {
			continue
		}
		if w.Repo == repo && w.PR == pr && w.HeadSHA == headSHA {
			return w
		}
	}
	return nil
}

// normalizeWatch prepares a brand new Watch for persistence. When no usable ID
// is supplied, or the supplied one is already taken by an active or closed
// Watch, the ID is derived from the alias so history is never overwritten and
// the same PR head always maps to one Watch.
func normalizeWatch(w Watch, known []Watch, now time.Time) Watch {
	if w.ID == "" || watchIDExists(known, w.ID) {
		w.ID = watchID(w.Repo, w.PR, w.HeadSHA, now)
	}
	if w.Status == "" {
		w.Status = StatusWatching
	}
	if w.Verdict == "" {
		w.Verdict = VerdictWatching
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	return w
}

// watchID builds a handle for the (repo, pr, headSHA) alias. The suffix records
// the issue time and the lease length so a stale handle can be recognised
// without a store read. Repo and headSHA keep the same escaping as ObligationKey;
// '@' and ':' separate the lease fields from the alias and from each other.
func watchID(repo string, pr int, headSHA string, issuedAt time.Time) string {
	return "watch:" + obligationKeyEscaper.Replace(repo) + ":" +
		strconv.Itoa(pr) + ":" + obligationKeyEscaper.Replace(headSHA) +
		"@" + strconv.FormatInt(issuedAt.Unix(), 10) +
		":" + strconv.FormatInt(int64(WatchHandleTTL/time.Second), 10)
}

// NewWatchHandle is the exported form of watchID. Callers outside domain use it
// to construct the same handle the fan-out derives for an alias.
func NewWatchHandle(repo string, pr int, headSHA string, issuedAt time.Time) string {
	return watchID(repo, pr, headSHA, issuedAt)
}

// ParseWatchHandle splits the lease suffix off a watchID. ok is false for a
// legacy handle with no lease and for a malformed suffix, so callers can keep
// the old behaviour for handles that carry no lifetime.
func ParseWatchHandle(handle string) (issuedAt time.Time, ttl time.Duration, ok bool) {
	at := strings.LastIndex(handle, "@")
	if at < 0 {
		return time.Time{}, 0, false
	}
	lease := handle[at+1:]
	colon := strings.LastIndex(lease, ":")
	if colon < 0 {
		return time.Time{}, 0, false
	}
	issuedSec, err := strconv.ParseInt(lease[:colon], 10, 64)
	if err != nil || issuedSec < 0 {
		return time.Time{}, 0, false
	}
	ttlSec, err := strconv.ParseInt(lease[colon+1:], 10, 64)
	if err != nil || ttlSec <= 0 {
		return time.Time{}, 0, false
	}
	return time.Unix(issuedSec, 0).UTC(), time.Duration(ttlSec) * time.Second, true
}

// WatchHandleExpired reports whether handle had expired at now. A handle with
// no lease never expires, so clients from before the lease keep working.
func WatchHandleExpired(handle string, now time.Time) bool {
	issuedAt, ttl, ok := ParseWatchHandle(handle)
	if !ok {
		return false
	}
	return !now.Before(issuedAt.Add(ttl))
}

func watchIDExists(watches []Watch, id string) bool {
	for i := range watches {
		if watches[i].ID == id {
			return true
		}
	}
	return false
}

func hasSubscriber(w Watch, id string) bool {
	for i := range w.Subscribers {
		if w.Subscribers[i].ID == id {
			return true
		}
	}
	return false
}

func countSubscriberWatches(watches []Watch, subscriberID string) int {
	n := 0
	for i := range watches {
		w := &watches[i]
		if w.Status == StatusClosed {
			continue
		}
		if hasSubscriber(*w, subscriberID) {
			n++
		}
	}
	return n
}
