// T1.6 property checks for the ten invariants of WORKER-PROMPT §4.2.
//
// One rapid.Check draws one command sequence; ten subtests run the same 1000
// sequences and check a different invariant after every command. The harness
// drives the real domain code (Fanout.Subscribe and Transition) through a
// domain.Store implemented in memory. Checks 7 and 9 use that Store's write
// counter and read-only methods as the card requires.
//
// Checks 8 and 10 encode the contract on domain types: the wake ledger and the
// verdict engine are not part of main yet (T1.4/T1.5/T1.8), so those two
// properties model the rule and feed its result into the real state machine.
package domain

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// propClock keeps every timestamp in the test deterministic.
type propClock struct {
	at time.Time
}

func (c propClock) Now() time.Time { return c.at }

// propCommit is one atomic write: the Watch with its verdict and every wake
// obligation for that revision in the same record (invariant 7).
type propCommit struct {
	watch       Watch
	obligations []Obligation
}

// propStore is the in-memory domain.Store used by the property harness. It
// counts writes so read-only operations can be proven write-free.
type propStore struct {
	watches     map[string]Watch
	obligations map[string]Obligation
	writes      int
	commits     []propCommit
}

func newPropStore() *propStore {
	return &propStore{
		watches:     make(map[string]Watch),
		obligations: make(map[string]Obligation),
	}
}

var _ Store = (*propStore)(nil)

func (s *propStore) SaveWatch(w Watch) error {
	s.writes++
	s.watches[w.ID] = copyWatch(w)
	return nil
}

func (s *propStore) Watch(id string) (Watch, error) {
	w, ok := s.watches[id]
	if !ok {
		return Watch{}, NewAppError(CodeWatchNotFound, "watch "+id+" not found")
	}
	return copyWatch(w), nil
}

func (s *propStore) ActiveWatches() ([]Watch, error) {
	var out []Watch
	for _, w := range s.watches {
		if w.Status != StatusClosed {
			out = append(out, copyWatch(w))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *propStore) AllWatches() ([]Watch, error) {
	out := make([]Watch, 0, len(s.watches))
	for _, w := range s.watches {
		out = append(out, copyWatch(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *propStore) SaveObligation(o Obligation) error {
	s.writes++
	s.obligations[o.ID] = copyObligation(o)
	return nil
}

func (s *propStore) Obligation(id string) (Obligation, error) {
	o, ok := s.obligations[id]
	if !ok {
		return Obligation{}, fmt.Errorf("obligation %s not found", id)
	}
	return copyObligation(o), nil
}

func (s *propStore) PendingObligations() ([]Obligation, error) {
	var out []Obligation
	for _, o := range s.obligations {
		if o.DeliveredAt.IsZero() {
			out = append(out, copyObligation(o))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// commit stages the verdict and all wake intents in one record. No response is
// recorded by the harness before this call returns.
func (s *propStore) commit(w Watch, subscribers []Subscriber, at time.Time) int {
	s.writes++
	rec := propCommit{watch: copyWatch(w)}
	for _, sub := range subscribers {
		o := Obligation{
			ID:             ObligationKey(w.ID, w.Revision, sub.ID),
			WatchID:        w.ID,
			Revision:       w.Revision,
			SubscriberID:   sub.ID,
			VerdictPointer: w.Verdict,
			StagedAt:       at,
		}
		s.obligations[o.ID] = o
		rec.obligations = append(rec.obligations, o)
	}
	s.watches[w.ID] = copyWatch(w)
	s.commits = append(s.commits, rec)
	return len(s.commits) - 1
}

type storeSnapshot struct {
	watches     map[string]Watch
	obligations map[string]Obligation
	writes      int
}

func (s *propStore) snapshot() storeSnapshot {
	watches := make(map[string]Watch, len(s.watches))
	for id, w := range s.watches {
		watches[id] = copyWatch(w)
	}
	obligations := make(map[string]Obligation, len(s.obligations))
	for id, o := range s.obligations {
		obligations[id] = copyObligation(o)
	}
	return storeSnapshot{watches: watches, obligations: obligations, writes: s.writes}
}

func copyWatch(w Watch) Watch {
	out := w
	if w.Checks != nil {
		out.Checks = make(map[string]Check, len(w.Checks))
		for k, v := range w.Checks {
			out.Checks[k] = v
		}
	}
	if w.Subscribers != nil {
		out.Subscribers = append([]Subscriber(nil), w.Subscribers...)
	}
	if w.Flaky.Names != nil {
		out.Flaky.Names = append([]string(nil), w.Flaky.Names...)
	}
	if w.Flaky.Used != nil {
		out.Flaky.Used = make(map[string]int, len(w.Flaky.Used))
		for k, v := range w.Flaky.Used {
			out.Flaky.Used[k] = v
		}
	}
	return out
}

func copyObligation(o Obligation) Obligation {
	c := o
	if o.Attempts != nil {
		c.Attempts = append([]Attempt(nil), o.Attempts...)
	}
	return c
}

// watchKey identifies one Watch instance. An alias may be reused after a
// close; CreatedAt separates the old instance from the new one.
type watchKey struct {
	id      string
	created int64
}

func keyOf(w Watch) watchKey {
	return watchKey{id: w.ID, created: w.CreatedAt.UnixNano()}
}

type obligationIdentity struct {
	id             string
	watchID        string
	revision       uint64
	subscriberID   string
	verdictPointer Verdict
	stagedAt       time.Time
}

func identityOf(o Obligation) obligationIdentity {
	return obligationIdentity{
		id:             o.ID,
		watchID:        o.WatchID,
		revision:       o.Revision,
		subscriberID:   o.SubscriberID,
		verdictPointer: o.VerdictPointer,
		stagedAt:       o.StagedAt,
	}
}

type observedWake struct {
	effect          Effect
	subscribers     []string
	commitIndex     int
	writesBefore    int
	writesAtReached int
}

type readyCase struct {
	cached    Verdict
	validated bool
	derived   Verdict
}

// invariantHarness applies generated commands to the real domain and remembers
// the observations the ten checks need.
type invariantHarness struct {
	store  *propStore
	fanout *Fanout
	clock  propClock
	step   int

	trace []string

	revisions    map[watchKey][]uint64
	wakeEffects  []Effect
	wakeKeys     []string
	cancelled    map[watchKey]Verdict
	cancelEvents int
	known        map[string]obligationIdentity
	opaque       map[string]bool
	wakes        []observedWake
	deliveries   map[string][]Attempt
	readyCases   []readyCase
}

func newInvariantHarness() *invariantHarness {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	store := newPropStore()
	return &invariantHarness{
		store:      store,
		fanout:     NewFanout(store, propClock{at: at}),
		clock:      propClock{at: at},
		revisions:  make(map[watchKey][]uint64),
		cancelled:  make(map[watchKey]Verdict),
		known:      make(map[string]obligationIdentity),
		opaque:     make(map[string]bool),
		deliveries: make(map[string][]Attempt),
	}
}

// cachedReady models the rule behind invariant 10 until the verdict engine
// lands: a cached ready may be reused only after suite composition was
// validated on the current head SHA. An unvalidated ready degrades to watching.
func cachedReady(cached Verdict, validated bool) Verdict {
	if cached == VerdictReady && !validated {
		return VerdictWatching
	}
	return cached
}

func (h *invariantHarness) apply(c command) {
	h.step++
	h.clock.at = h.clock.at.Add(time.Second)
	h.trace = append(h.trace, c.String())
	h.opaque[c.Target] = true
	h.opaque[c.SessionID] = true
	writesBefore := h.store.writes
	wakesBefore := len(h.wakes)

	switch c.Kind {
	case cmdWatchPR:
		// A fresh client-supplied ID keeps history of closed watches intact;
		// Subscribe still finds an existing active Watch by (repo, pr, headSHA).
		sub := Subscriber{
			ID:      c.SubscriberID,
			Role:    c.Role,
			Adapter: "property",
			Target:  c.Target,
		}
		_, _ = h.fanout.Subscribe(
			Watch{ID: fmt.Sprintf("generated-%d", h.step), Repo: c.Repo, PR: c.PR, HeadSHA: c.HeadSHA},
			sub,
		)
	case cmdWaitResult:
		// wait_result is a read: return state without staging or claiming.
		if w, ok := h.pickWatch(c.WatchIndex); ok {
			_, _ = h.store.Watch(w.ID)
		}
		// Later receipts advance the wake ladder. The generated outcome decides
		// whether another attempt is made; recordDelivery applies the rule that
		// posted and indeterminate end the ladder.
		keys := make([]string, 0, len(h.deliveries))
		for key := range h.deliveries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			h.recordDelivery(key, c.Outcome, h.clock.at)
		}
	case cmdAck:
		if id, ok := h.pickObligation(c.ObligationIndex); ok {
			o, err := h.store.Obligation(id)
			if err == nil {
				o.ClaimedAt = h.clock.at
				_ = h.store.SaveObligation(o)
			}
		}
	case cmdCancel:
		w, ok := h.pickWatch(c.WatchIndex)
		if !ok {
			break
		}
		next, effects := Transition(w, CancelEvent(h.clock.at))
		h.cancelEvents += len(effects)
		h.saveIfChanged(w, next)
		if next.CloseReason == string(CloseReasonCancelled) {
			h.cancelled[keyOf(w)] = next.Verdict
		}
	case cmdTick:
		w, ok := h.pickWatch(c.WatchIndex)
		if !ok {
			break
		}
		derived := cachedReady(c.Verdict, c.Validated)
		h.readyCases = append(h.readyCases, readyCase{
			cached: c.Verdict, validated: c.Validated, derived: derived,
		})
		next, effects := Transition(w, VerdictEvent(derived, h.clock.at))
		h.saveIfChanged(w, next)
		for _, e := range effects {
			if e.Kind != EffectWakeSubscribers {
				continue
			}
			h.wakeEffects = append(h.wakeEffects, e)
			subs := copyWatch(next).Subscribers
			commitIndex := h.store.commit(next, subs, h.clock.at)
			ids := make([]string, 0, len(subs))
			for _, sub := range subs {
				key := ObligationKey(e.WatchID, e.Revision, sub.ID)
				h.wakeKeys = append(h.wakeKeys, key)
				ids = append(ids, key)
				h.recordDelivery(key, c.Outcome, h.clock.at)
			}
			h.wakes = append(h.wakes, observedWake{
				effect:       e,
				subscribers:  ids,
				commitIndex:  commitIndex,
				writesBefore: writesBefore,
			})
		}
	case cmdHeadChanged:
		w, ok := h.pickWatch(c.WatchIndex)
		if !ok {
			break
		}
		next, effects := Transition(w, HeadChangedEvent(c.NewHeadSHA, h.clock.at))
		h.saveIfChanged(w, next)
		for _, e := range effects {
			if e.Kind != EffectCreateWatch {
				continue
			}
			for _, sub := range e.Subscribers {
				_, _ = h.fanout.Subscribe(
					Watch{ID: fmt.Sprintf("successor-%d", h.step), Repo: w.Repo, PR: w.PR, HeadSHA: e.HeadSHA},
					sub,
				)
			}
		}
	}

	// Record the state after the command, before the next "response".
	all, _ := h.store.AllWatches()
	for _, w := range all {
		h.revisions[keyOf(w)] = append(h.revisions[keyOf(w)], w.Revision)
	}
	for _, o := range h.store.obligations {
		h.known[o.ID] = identityOf(o)
	}
	for i := wakesBefore; i < len(h.wakes); i++ {
		h.wakes[i].writesAtReached = h.store.writes
	}
}

func (h *invariantHarness) saveIfChanged(before, after Watch) {
	if !reflect.DeepEqual(before, after) {
		_ = h.store.SaveWatch(after)
	}
}

func (h *invariantHarness) pickWatch(index int) (Watch, bool) {
	all, _ := h.store.AllWatches()
	if len(all) == 0 {
		return Watch{}, false
	}
	return all[index%len(all)], true
}

func (h *invariantHarness) pickObligation(index int) (string, bool) {
	ids := make([]string, 0, len(h.store.obligations))
	for id := range h.store.obligations {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", false
	}
	sort.Strings(ids)
	return ids[index%len(ids)], true
}

// recordDelivery is the wake-ladder model behind invariant 8: posted and
// indeterminate end the ladder; only transport-failed may retry, at most three
// attempts per obligation.
func (h *invariantHarness) recordDelivery(key string, outcome AttemptOutcome, at time.Time) {
	attempts := h.deliveries[key]
	if len(attempts) > 0 {
		last := attempts[len(attempts)-1].Outcome
		if last == OutcomePosted || last == OutcomeIndeterminate || len(attempts) >= 3 {
			return
		}
	}
	attempts = append(attempts, Attempt{
		N:       len(attempts) + 1,
		At:      at,
		Channel: "property",
		Outcome: outcome,
	})
	h.deliveries[key] = attempts
}

type invariantCheck func(*invariantHarness) error

// 1. At most one active Watch per (repo, pr, headSHA).
func checkOneActiveWatch(h *invariantHarness) error {
	type alias struct {
		repo string
		pr   int
		sha  string
	}
	active, err := h.store.ActiveWatches()
	if err != nil {
		return err
	}
	seen := make(map[alias]string, len(active))
	for _, w := range active {
		a := alias{repo: w.Repo, pr: w.PR, sha: w.HeadSHA}
		if previous, ok := seen[a]; ok {
			return fmt.Errorf("two active watches for %+v: %s and %s", a, previous, w.ID)
		}
		seen[a] = w.ID
	}
	return nil
}

// 2. At-least-once wake with exactly one effect per watchID+revision+subscriberID.
func checkWakeIdempotency(h *invariantHarness) error {
	seen := make(map[string]bool, len(h.wakeKeys))
	for _, key := range h.wakeKeys {
		if seen[key] {
			return fmt.Errorf("duplicate wake effect for key %s", key)
		}
		seen[key] = true
	}
	for _, key := range h.wakeKeys {
		o, err := h.store.Obligation(key)
		if err != nil {
			return fmt.Errorf("wake key %s was not persisted: %v", key, err)
		}
		if o.ID != key || o.SubscriberID == "" {
			return fmt.Errorf("wake key %s persisted as %+v", key, o)
		}
	}
	for id, o := range h.store.obligations {
		want := ObligationKey(o.WatchID, o.Revision, o.SubscriberID)
		if id != want {
			return fmt.Errorf("obligation id %s does not match key %s", id, want)
		}
	}
	return nil
}

// 3. Revision never decreases and no value is reused.
func checkRevisionMonotonic(h *invariantHarness) error {
	for key, seq := range h.revisions {
		if len(seq) == 0 {
			continue
		}
		highest := seq[0]
		for _, rev := range seq[1:] {
			if rev < highest {
				return fmt.Errorf("watch %s revision went from %d back to %d", key.id, highest, rev)
			}
			if rev > highest {
				highest = rev
			}
		}
	}
	return nil
}

// 4. A cancelled Watch never becomes ready and cancel emits no wake effect.
func checkCancelledNotReady(h *invariantHarness) error {
	if h.cancelEvents != 0 {
		return fmt.Errorf("cancel produced %d wake effects", h.cancelEvents)
	}
	for key, verdictAtCancel := range h.cancelled {
		w, err := h.store.Watch(key.id)
		if err != nil {
			return err
		}
		// The alias may have been reused by a newer Watch. That is a different
		// instance, so the cancelled one is no longer in the store.
		if keyOf(w) != key {
			continue
		}
		if w.Status != StatusClosed || w.CloseReason != string(CloseReasonCancelled) {
			return fmt.Errorf("cancelled watch %s is %s (%s)", key.id, w.Status, w.CloseReason)
		}
		if w.Verdict != verdictAtCancel {
			return fmt.Errorf("cancelled watch %s verdict changed from %s to %s", key.id, verdictAtCancel, w.Verdict)
		}
		if verdictAtCancel != VerdictReady && w.Verdict == VerdictReady {
			return fmt.Errorf("cancelled watch %s became ready", key.id)
		}
	}
	return nil
}

// 5. The identity and verdict of one obligation never change after creation.
func checkObligationStable(h *invariantHarness) error {
	for id, want := range h.known {
		o, err := h.store.Obligation(id)
		if err != nil {
			return err
		}
		if got := identityOf(o); got != want {
			return fmt.Errorf("obligation %s response changed: got %+v, want %+v", id, got, want)
		}
	}
	return nil
}

// 6. The core stores target verbatim and keys obligations without it.
// sessionID is not a domain field; generating it here proves the domain does
// not need to interpret it to register or wake a subscriber.
func checkOpaquePayloads(h *invariantHarness) error {
	all, err := h.store.AllWatches()
	if err != nil {
		return err
	}
	for _, w := range all {
		for _, sub := range w.Subscribers {
			if !h.opaque[sub.Target] {
				return fmt.Errorf("subscriber %s target %q was transformed", sub.ID, sub.Target)
			}
			key := ObligationKey(w.ID, w.Revision, sub.ID)
			o, err := h.store.Obligation(key)
			if err == nil && (o.SubscriberID != sub.ID || o.Revision != w.Revision) {
				return fmt.Errorf("obligation %s does not match subscriber %s at revision %d", key, sub.ID, w.Revision)
			}
		}
	}
	return nil
}

// 7. State and wake intent are committed before the response.
func checkCommitBeforeResponse(h *invariantHarness) error {
	for _, wake := range h.wakes {
		if wake.commitIndex < 0 || wake.commitIndex >= len(h.store.commits) {
			return fmt.Errorf("wake %+v has no commit record", wake.effect)
		}
		if wake.writesAtReached <= wake.writesBefore {
			return fmt.Errorf("response for wake %+v was recorded before any write", wake.effect)
		}
		rec := h.store.commits[wake.commitIndex]
		if rec.watch.ID != wake.effect.WatchID ||
			rec.watch.Revision != wake.effect.Revision ||
			rec.watch.Verdict != wake.effect.Verdict {
			return fmt.Errorf("commit %+v does not carry wake effect %+v", rec.watch, wake.effect)
		}
		for _, key := range wake.subscribers {
			found := false
			for _, o := range rec.obligations {
				if o.ID == key && o.VerdictPointer == wake.effect.Verdict {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("wake intent %s missing from commit for %s", key, wake.effect.WatchID)
			}
		}
	}
	return nil
}

// 8. An indeterminate delivery is never retried.
func checkIndeterminateNoRetry(h *invariantHarness) error {
	for key, attempts := range h.deliveries {
		if len(attempts) > 3 {
			return fmt.Errorf("obligation %s retried %d times", key, len(attempts))
		}
		stopped := false
		for i, a := range attempts {
			if a.N != i+1 {
				return fmt.Errorf("obligation %s attempt numbers are not sequential: %+v", key, attempts)
			}
			if stopped {
				return fmt.Errorf("obligation %s retried after terminal outcome: %+v", key, attempts)
			}
			if a.Outcome == OutcomeIndeterminate || a.Outcome == OutcomePosted {
				stopped = true
			}
		}
	}
	return nil
}

// 9. list_running and stats (the Store read methods) never write state.
func checkReadsDoNotWrite(h *invariantHarness) error {
	before := h.store.snapshot()
	_, _ = h.store.ActiveWatches()
	_, _ = h.store.AllWatches()
	_, _ = h.store.PendingObligations()
	after := h.store.snapshot()
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("read methods changed store: writes %d -> %d", before.writes, after.writes)
	}
	return nil
}

// 10. A cached ready is never served without suite validation.
func checkReadyFromCache(h *invariantHarness) error {
	for _, rc := range h.readyCases {
		if rc.cached == VerdictReady && !rc.validated && rc.derived == VerdictReady {
			return fmt.Errorf("ready served from cache without suite validation: %+v", rc)
		}
	}
	return nil
}

func TestInvariants(t *testing.T) {
	checks := []struct {
		name  string
		check invariantCheck
	}{
		{"one_active_watch_per_alias", checkOneActiveWatch},
		{"wake_idempotency", checkWakeIdempotency},
		{"revision_monotonic", checkRevisionMonotonic},
		{"cancelled_not_ready", checkCancelledNotReady},
		{"obligation_stable", checkObligationStable},
		{"opaque_payloads", checkOpaquePayloads},
		{"commit_before_response", checkCommitBeforeResponse},
		{"indeterminate_no_retry", checkIndeterminateNoRetry},
		{"reads_do_not_write", checkReadsDoNotWrite},
		{"no_ready_from_cache", checkReadyFromCache},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			runInvariantProperty(t, func(rt *rapid.T) {
				h := newInvariantHarness()
				seq := commandSeqGen.Draw(rt, "commands")
				for _, c := range seq {
					h.apply(c)
					if err := tc.check(h); err != nil {
						rt.Fatalf(
							"invariant %s violated: %v\nbase seed=%d (rapid prints the seed to reproduce)\nsequence (%d commands): %s",
							tc.name, err, invariantSeed, len(seq), shortenTrace(h.trace),
						)
					}
				}
			})
		})
	}
}
