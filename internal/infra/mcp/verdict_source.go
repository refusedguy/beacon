package mcp

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// verdictWatchLister is the read-only store port the poller needs. It is
// declared here so this package never imports internal/infra/store (ADR 0005).
// *store.DB satisfies it and the composition root passes it in.
type verdictWatchLister interface {
	LiveWatches() ([]domain.Watch, error)
}

// verdictSubBuffer bounds one subscriber channel. A slow caller cannot stall
// the shared poller: send() selects on the subscription's done channel and
// returns as soon as the caller departs.
const verdictSubBuffer = 32

// DBVerdictSource is the production VerdictSource. One instance polls the live
// watches at a fixed interval and fans every change out to the subscriptions
// of the affected watchID. With no registered subscriber the loop skips the
// store read, so an idle server does not hammer LiveWatches. Subscribe and
// Close are safe from any goroutine, and Close never blocks another subscriber.
type DBVerdictSource struct {
	watches  verdictWatchLister
	interval time.Duration
	now      func() time.Time

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}

	mu   sync.Mutex
	last map[string]verdictState
	subs map[string]map[*dbVerdictSubscription]struct{}
}

// verdictState is the last observed value of the fields that matter to a
// waiter. Checks are copied into it so a caller cannot mutate the source.
type verdictState struct {
	revision uint64
	status   domain.Status
	verdict  domain.Verdict
	checks   map[string]domain.Check
}

var _ VerdictSource = (*DBVerdictSource)(nil)

// NewDBVerdictSource starts one poller over watches. A non-positive interval
// disables the background loop; callers that use that mode drive pollOnce
// directly. The background loop reads the store only while at least one
// subscriber is registered. Stop always releases the loop, so tests can wait
// for its exit.
func NewDBVerdictSource(watches verdictWatchLister, interval time.Duration) *DBVerdictSource {
	s := &DBVerdictSource{
		watches:  watches,
		interval: interval,
		now:      time.Now,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		last:     make(map[string]verdictState),
		subs:     make(map[string]map[*dbVerdictSubscription]struct{}),
	}
	if interval > 0 {
		go s.loop()
	} else {
		close(s.done)
	}
	return s
}

// Stop ends the poller. It is idempotent and waits for the goroutine to exit,
// so a caller can close the store only after the loop no longer reads it.
func (s *DBVerdictSource) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// hasSubscribers reports whether any wait_result caller is still listening.
// The map belongs to the source mutex, so the check is race-free with
// Subscribe and Close.
func (s *DBVerdictSource) hasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

func (s *DBVerdictSource) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			// An idle server has nothing to publish. Skipping the read keeps
			// LiveWatches off the ticker's hot path until a waiter arrives.
			if s.hasSubscribers() {
				s.pollOnce()
			}
		}
	}
}

// pollOnce reads one snapshot and publishes every Watch whose revision,
// status, verdict or checks changed since the previous snapshot. A Watch seen
// for the first time is also published, which gives subscribers that arrived
// before the first tick the current state.
func (s *DBVerdictSource) pollOnce() {
	if s.watches == nil {
		return
	}
	all, err := s.watches.LiveWatches()
	if err != nil {
		return
	}

	seen := make(map[string]struct{}, len(all))
	for _, w := range all {
		if w.ID == "" {
			continue
		}
		seen[w.ID] = struct{}{}
		cur := verdictState{revision: w.Revision, status: w.Status, verdict: w.Verdict, checks: w.Checks}
		s.mu.Lock()
		prev, known := s.last[w.ID]
		changed := !known || !verdictStateEqual(prev, cur)
		if changed {
			s.last[w.ID] = cloneVerdictState(cur)
		}
		subs := subscriptionSnapshot(s.subs[w.ID])
		s.mu.Unlock()
		if !changed {
			continue
		}
		update := WaitVerdictUpdate{
			WatchID:  w.ID,
			Status:   w.Status,
			Verdict:  w.Verdict,
			Revision: w.Revision,
			Checks:   cloneChecks(w.Checks),
			At:       s.timestamp(),
			Kind:     verdictEventKind(w.Verdict),
		}
		for _, sub := range subs {
			sub.send(update)
		}
	}

	// A Watch that left the live set must look new when it comes back; keeping
	// its old state would suppress its first update after a re-registration.
	s.mu.Lock()
	for id := range s.last {
		if _, ok := seen[id]; !ok {
			delete(s.last, id)
		}
	}
	s.mu.Unlock()
}

// Subscribe registers one caller for watchID. If the source already knows a
// terminal or parked state for that Watch, the new subscriber receives it
// immediately. That closes the gap between the wait loop's initial store read
// and this registration, without replaying harmless watching ticks.
func (s *DBVerdictSource) Subscribe(watchID string) VerdictSubscription {
	sub := &dbVerdictSubscription{
		watchID: watchID,
		updates: make(chan WaitVerdictUpdate, verdictSubBuffer),
		done:    make(chan struct{}),
		source:  s,
	}
	s.mu.Lock()
	if s.subs[watchID] == nil {
		s.subs[watchID] = make(map[*dbVerdictSubscription]struct{})
	}
	s.subs[watchID][sub] = struct{}{}
	var seed *WaitVerdictUpdate
	if st, ok := s.last[watchID]; ok && (isWaitResultVerdict(st.verdict) || isNeedsInput(st.status, st.verdict)) {
		seed = &WaitVerdictUpdate{
			WatchID:  watchID,
			Status:   st.status,
			Verdict:  st.verdict,
			Revision: st.revision,
			Checks:   cloneChecks(st.checks),
			At:       s.timestamp(),
			Kind:     verdictEventKind(st.verdict),
		}
	}
	s.mu.Unlock()
	if seed != nil {
		sub.send(*seed)
	}
	return sub
}

func (s *DBVerdictSource) timestamp() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

type dbVerdictSubscription struct {
	watchID string
	updates chan WaitVerdictUpdate
	done    chan struct{}
	source  *DBVerdictSource
	once    sync.Once
}

func (s *dbVerdictSubscription) Updates() <-chan WaitVerdictUpdate { return s.updates }

// Close removes this subscriber and releases a poller blocked on its channel.
// It never touches the other subscribers of the same Watch.
func (s *dbVerdictSubscription) Close() {
	s.once.Do(func() {
		close(s.done)
		s.source.mu.Lock()
		if set := s.source.subs[s.watchID]; set != nil {
			delete(set, s)
			if len(set) == 0 {
				delete(s.source.subs, s.watchID)
			}
		}
		s.source.mu.Unlock()
	})
}

func (s *dbVerdictSubscription) send(u WaitVerdictUpdate) {
	select {
	case <-s.done:
		return
	default:
	}
	// One caller must not be able to mutate the map another caller received.
	u.Checks = cloneChecks(u.Checks)
	select {
	case <-s.done:
	case s.updates <- u:
	}
}

func verdictEventKind(v domain.Verdict) WaitEventKind {
	if isWaitResultVerdict(v) {
		return WaitEventCompleted
	}
	return WaitEventProgress
}

func subscriptionSnapshot(set map[*dbVerdictSubscription]struct{}) []*dbVerdictSubscription {
	if len(set) == 0 {
		return nil
	}
	return slices.Collect(maps.Keys(set))
}

func verdictStateEqual(a, b verdictState) bool {
	return a.revision == b.revision && a.status == b.status &&
		a.verdict == b.verdict && checksEqual(a.checks, b.checks)
}

func cloneVerdictState(s verdictState) verdictState {
	s.checks = cloneChecks(s.checks)
	return s
}

// checksEqual treats nil and empty as the same observation, so a Watch whose
// checks were cleared does not publish a spurious update on that difference
// alone. domain.Check is comparable, so a plain value comparison is enough.
func checksEqual(a, b map[string]domain.Check) bool {
	if len(a) != len(b) {
		return false
	}
	for name, check := range a {
		if other, ok := b[name]; !ok || other != check {
			return false
		}
	}
	return true
}

func cloneChecks(in map[string]domain.Check) map[string]domain.Check {
	if in == nil {
		return nil
	}
	out := make(map[string]domain.Check, len(in))
	for name, check := range in {
		out[name] = check
	}
	return out
}
