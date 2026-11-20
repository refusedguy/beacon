// W9-A property checks: ten Beacon invariants driven by 1000 random event
// sequences each (pgregory.net/rapid). The harness runs the real domain code
// (Fanout.Subscribe, Transition, EvaluatePR and the role/identity helpers)
// against an in-memory Store, so the run needs no network, clock or database.
//
// The flag pinning, the in-memory propStore/propClock and the copy helpers are
// shared with generators_property_test.go and invariants_property_test.go in
// this package. This file adds a distinct event stream and ten distinct checks.
package domain

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// ---- generated events -----------------------------------------------------

type w9EventKind string

const (
	w9Register     w9EventKind = "register"
	w9Verdict      w9EventKind = "verdict"
	w9Cancel       w9EventKind = "cancel"
	w9HeadChanged  w9EventKind = "head_changed"
	w9Block        w9EventKind = "block"
	w9ResolveBlock w9EventKind = "resolve_blocker"
	w9Read         w9EventKind = "read"
	w9EvaluatePR   w9EventKind = "evaluate_pr"
)

// w9Event is one operation in a generated sequence. Fields that do not apply to
// Kind keep their zero value. Indices are drawn, not resolved: the harness maps
// them onto whatever it currently holds.
type w9Event struct {
	Kind         w9EventKind
	Repo         string
	PR           int
	SHA          string
	NewSHA       string
	SubscriberID string
	Role         Role
	WatchIndex   int
	Verdict      Verdict
	Outcome      AttemptOutcome
	BlockerIndex int
	State        string
}

func (e w9Event) String() string {
	switch e.Kind {
	case w9Register:
		return fmt.Sprintf("register{repo=%s pr=%d sha=%s sub=%s role=%s}", e.Repo, e.PR, e.SHA, e.SubscriberID, e.Role)
	case w9Verdict:
		return fmt.Sprintf("verdict{watch#%d v=%s outcome=%s}", e.WatchIndex, e.Verdict, e.Outcome)
	case w9Cancel:
		return fmt.Sprintf("cancel{watch#%d}", e.WatchIndex)
	case w9HeadChanged:
		return fmt.Sprintf("head_changed{watch#%d sha=%s}", e.WatchIndex, e.NewSHA)
	case w9Block:
		return fmt.Sprintf("block{watch#%d}", e.WatchIndex)
	case w9ResolveBlock:
		return fmt.Sprintf("resolve_blocker#%d", e.BlockerIndex)
	case w9Read:
		return "read"
	case w9EvaluatePR:
		return fmt.Sprintf("evaluate_pr{state=%s}", e.State)
	default:
		return string(e.Kind)
	}
}

var (
	w9RepoGen = rapid.SampledFrom([]string{"org/repo", "org/fork"})
	w9PRGen   = rapid.IntRange(1, 3)
	w9SHAGen  = rapid.SampledFrom([]string{"sha-a", "sha-b", "sha-c"})
	w9SubGen  = rapid.SampledFrom([]string{"executor", "orchestrator", "fixer", "sub:x"})
	// RoleFixer is drawn on purpose: invariant 7 needs a fixer to be a valid
	// registration and wake addressee in ordinary sequences.
	w9RoleGen    = rapid.SampledFrom([]Role{RoleExecutor, RoleOrchestrator, RoleFixer})
	w9VerdictGen = rapid.SampledFrom([]Verdict{
		VerdictWatching, VerdictReady, VerdictRebase, VerdictRetry,
		VerdictKeepWorking, VerdictTimeout, VerdictMerged, VerdictClosed,
	})
	w9OutcomeGen = rapid.SampledFrom([]AttemptOutcome{
		OutcomePosted, OutcomeTransportFailed, OutcomeIndeterminate,
	})
	w9IndexGen = rapid.IntRange(0, 7)
	w9StateGen = rapid.SampledFrom([]string{"MERGED", "CLOSED", "OPEN", "draft"})
	w9KindGen  = rapid.SampledFrom([]w9EventKind{
		w9Register, w9Verdict, w9Cancel, w9HeadChanged,
		w9Block, w9ResolveBlock, w9Read, w9EvaluatePR,
	})

	w9EventGen = rapid.Custom(func(t *rapid.T) w9Event {
		return w9Event{
			Kind:         w9KindGen.Draw(t, "kind"),
			Repo:         w9RepoGen.Draw(t, "repo"),
			PR:           w9PRGen.Draw(t, "pr"),
			SHA:          w9SHAGen.Draw(t, "sha"),
			NewSHA:       w9SHAGen.Draw(t, "new_sha"),
			SubscriberID: w9SubGen.Draw(t, "subscriber"),
			Role:         w9RoleGen.Draw(t, "role"),
			WatchIndex:   w9IndexGen.Draw(t, "watch_index"),
			Verdict:      w9VerdictGen.Draw(t, "verdict"),
			Outcome:      w9OutcomeGen.Draw(t, "outcome"),
			BlockerIndex: w9IndexGen.Draw(t, "blocker_index"),
			State:        w9StateGen.Draw(t, "state"),
		}
	})

	// One rapid.Check draws one sequence; the ten subtests replay the same 1000
	// sequences against the same pinned seed.
	w9EventSeqGen = rapid.SliceOfN(w9EventGen, 1, 40)
)

// ---- harness --------------------------------------------------------------

type w9Alias struct {
	repo string
	pr   int
	sha  string
}

type w9FixerReg struct {
	watchID  string
	subID    string
	revision uint64
}

type w9EvalCase struct {
	state   string
	verdict Verdict
	wakeLen int
}

type w9UnblockExpectation struct {
	watchID  string
	revision uint64
}

// w9Harness applies events to the real domain and records the observations the
// ten checks need. Violations found while applying an event are kept as sticky
// strings so the deferred checks can surface them with the shrinking trace.
type w9Harness struct {
	store  *propStore
	fanout *Fanout
	clock  propClock
	step   int
	trace  []string

	revisions  map[watchKey][]uint64
	known      map[string]obligationIdentity
	deliveries map[string][]Attempt

	cancelled     map[watchKey]Verdict
	cancelWakes   int
	terminalWakes []Effect
	evalCases     []w9EvalCase
	fixerRegs     []w9FixerReg
	incidents     map[string]*BlockerIncident

	// unblockExpectations is reset at the start of every event so it only ever
	// describes the most recent resolve, before a later block can re-block the
	// same Watch.
	unblockExpectations []w9UnblockExpectation

	registerViolation  string
	structureViolation string
}

func newW9Harness() *w9Harness {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store := newPropStore()
	return &w9Harness{
		store:      store,
		fanout:     NewFanout(store, propClock{at: at}),
		clock:      propClock{at: at},
		revisions:  make(map[watchKey][]uint64),
		known:      make(map[string]obligationIdentity),
		deliveries: make(map[string][]Attempt),
		cancelled:  make(map[watchKey]Verdict),
		incidents:  make(map[string]*BlockerIncident),
	}
}

func (h *w9Harness) setRegisterViolation(format string, args ...interface{}) {
	if h.registerViolation == "" {
		h.registerViolation = fmt.Sprintf(format, args...)
	}
}

func (h *w9Harness) setStructureViolation(format string, args ...interface{}) {
	if h.structureViolation == "" {
		h.structureViolation = fmt.Sprintf(format, args...)
	}
}

func (h *w9Harness) apply(e w9Event) {
	h.step++
	h.clock.at = h.clock.at.Add(time.Second)
	h.trace = append(h.trace, e.String())
	h.unblockExpectations = nil

	switch e.Kind {
	case w9Register:
		h.applyRegister(e)
	case w9Verdict:
		h.applyVerdict(e)
	case w9Cancel:
		h.applyCancel(e)
	case w9HeadChanged:
		h.applyHeadChanged(e)
	case w9Block:
		h.applyBlock(e)
	case w9ResolveBlock:
		h.applyResolveBlocker(e)
	case w9Read:
		h.applyReads()
	case w9EvaluatePR:
		h.applyEvaluatePR(e)
	}

	// Snapshot post-state once, before the next response changes it.
	all, _ := h.store.AllWatches()
	for _, w := range all {
		h.revisions[keyOf(w)] = append(h.revisions[keyOf(w)], w.Revision)
	}
	for id, o := range h.store.obligations {
		// Keep the first observation as the baseline so a later mutation of an
		// existing obligationID is detected (invariant 4).
		if _, seen := h.known[id]; !seen {
			h.known[id] = identityOf(o)
		}
	}
}

// applyRegister subscribes a watch and repeats the identical registration. The
// second call must be a pure renewal: same Watch, same revision, same obligation
// (invariant 10). A fixer registration is recorded for invariant 7.
func (h *w9Harness) applyRegister(e w9Event) {
	watch := Watch{ID: fmt.Sprintf("w9-%d", h.step), Repo: e.Repo, PR: e.PR, HeadSHA: e.SHA}
	sub := Subscriber{
		ID:      e.SubscriberID,
		Role:    e.Role,
		Adapter: "w9",
		Target:  "ses_" + e.SubscriberID,
	}
	first, err1 := h.fanout.Subscribe(watch, sub)
	second, err2 := h.fanout.Subscribe(watch, sub)
	if err1 != nil || err2 != nil {
		h.setRegisterViolation("subscribe failed: first=%v second=%v", err1, err2)
		return
	}
	if first.Watch.ID != second.Watch.ID || first.Watch.Revision != second.Watch.Revision {
		h.setRegisterViolation("repeated register changed watch: %s@%d -> %s@%d",
			first.Watch.ID, first.Watch.Revision, second.Watch.ID, second.Watch.Revision)
	}
	if !second.Renewed {
		h.setRegisterViolation("repeated register of %s was not renewed", second.Watch.ID)
	}
	if first.Obligation.ID != second.Obligation.ID {
		h.setRegisterViolation("repeated register changed obligation: %s -> %s",
			first.Obligation.ID, second.Obligation.ID)
	}
	// A renewal keeps the subscriber's original role, so only record a fixer
	// when the watch really holds this subscriber with the fixer role.
	for _, sub := range first.Watch.Subscribers {
		if sub.ID == e.SubscriberID && sub.Role == RoleFixer {
			h.fixerRegs = append(h.fixerRegs, w9FixerReg{
				watchID:  first.Watch.ID,
				subID:    e.SubscriberID,
				revision: first.Watch.Revision,
			})
			break
		}
	}
}

func (h *w9Harness) applyVerdict(e w9Event) {
	w, ok := h.pickWatch(e.WatchIndex)
	if !ok {
		return
	}
	next, effects := Transition(w, VerdictEvent(e.Verdict, h.clock.at))
	h.saveIfChanged(w, next)

	if isTerminalVerdict(e.Verdict) {
		for _, eff := range effects {
			if eff.Kind == EffectWakeSubscribers {
				h.terminalWakes = append(h.terminalWakes, eff)
			}
		}
	}
	for _, eff := range effects {
		if eff.Kind != EffectWakeSubscribers {
			continue
		}
		subs := copyWatch(next).Subscribers
		_ = h.store.commit(next, subs, h.clock.at)
		for _, sub := range subs {
			key := ObligationKey(eff.WatchID, eff.Revision, sub.ID)
			h.recordDelivery(key, e.Outcome, h.clock.at)
		}
	}
}

func (h *w9Harness) applyCancel(e w9Event) {
	w, ok := h.pickWatch(e.WatchIndex)
	if !ok {
		return
	}
	next, effects := Transition(w, CancelEvent(h.clock.at))
	for _, eff := range effects {
		if eff.Kind == EffectWakeSubscribers {
			h.cancelWakes++
		}
	}
	h.saveIfChanged(w, next)
	if next.CloseReason == string(CloseReasonCancelled) {
		h.cancelled[keyOf(w)] = next.Verdict
	}
}

func (h *w9Harness) applyHeadChanged(e w9Event) {
	w, ok := h.pickWatch(e.WatchIndex)
	if !ok {
		return
	}
	next, effects := Transition(w, HeadChangedEvent(e.NewSHA, h.clock.at))
	h.saveIfChanged(w, next)
	for _, eff := range effects {
		if eff.Kind != EffectCreateWatch {
			continue
		}
		for _, sub := range eff.Subscribers {
			_, _ = h.fanout.Subscribe(
				Watch{ID: fmt.Sprintf("w9-successor-%d", h.step), Repo: w.Repo, PR: w.PR, HeadSHA: eff.HeadSHA},
				sub,
			)
		}
	}
}

// applyBlock blocks a live watch and links it to an open blocker incident,
// creating one when none exists. Blocking must not stage any wake effect.
func (h *w9Harness) applyBlock(e w9Event) {
	w, ok := h.pickWatch(e.WatchIndex)
	if !ok {
		return
	}
	info := BlockedInfo{
		Reason:       "ci failed on base",
		FailedChecks: []string{"ci/test"},
		Since:        h.clock.at,
		BlockedBy:    "w9",
	}
	next, effects := Transition(w, BlockEvent(info, h.clock.at))
	if len(effects) != 0 {
		h.setStructureViolation("block staged %d effects", len(effects))
	}
	wasBlocked := w.Status == StatusBlocked
	h.saveIfChanged(w, next)
	if !wasBlocked && next.Status == StatusBlocked {
		h.linkBlockedWatch(next.ID)
	}
}

// applyResolveBlocker resolves an incident and unblocks every linked Watch
// that is still blocked. This is the domain-level contract behind the control
// plane's ResolveBlocker (invariant 8): each unblock is the real UnblockEvent
// transition, so a Watch that was blocked by the incident ends up watching.
func (h *w9Harness) applyResolveBlocker(e w9Event) {
	inc := h.pickIncident(e.BlockerIndex)
	if inc == nil || inc.Status != BlockerStatusOpen {
		return
	}
	inc.Status = BlockerStatusResolved
	inc.ResolvedAt = h.clock.at
	for _, id := range inc.BlockedWatchIDs {
		w, err := h.store.Watch(id)
		if err != nil || w.Status != StatusBlocked {
			continue
		}
		next, effects := Transition(w, UnblockEvent(h.clock.at))
		if len(effects) != 0 {
			h.setStructureViolation("unblock staged %d effects", len(effects))
		}
		_ = h.store.SaveWatch(next)
		h.unblockExpectations = append(h.unblockExpectations, w9UnblockExpectation{
			watchID:  id,
			revision: w.Revision + 1,
		})
	}
}

func (h *w9Harness) applyReads() {
	_, _ = h.store.ActiveWatches()
	_, _ = h.store.AllWatches()
	_, _ = h.store.PendingObligations()
	ids := make([]string, 0, len(h.store.obligations))
	for id := range h.store.obligations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		_, _ = h.store.Obligation(id)
	}
}

func (h *w9Harness) applyEvaluatePR(e w9Event) {
	s := Snapshot{State: e.State}
	verdict, wake := EvaluatePR(s, Policy{})
	if e.State == "MERGED" || e.State == "CLOSED" {
		h.evalCases = append(h.evalCases, w9EvalCase{
			state: e.State, verdict: verdict, wakeLen: len(wake),
		})
	}
}

func (h *w9Harness) linkBlockedWatch(watchID string) {
	ids := make([]string, 0, len(h.incidents))
	for id := range h.incidents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		inc := h.incidents[id]
		if inc.Status != BlockerStatusOpen {
			continue
		}
		if !containsString(inc.BlockedWatchIDs, watchID) {
			inc.BlockedWatchIDs = append(inc.BlockedWatchIDs, watchID)
		}
		return
	}
	id := fmt.Sprintf("blocker-%d", h.step)
	h.incidents[id] = &BlockerIncident{
		ID:              id,
		Status:          BlockerStatusOpen,
		BlockedWatchIDs: []string{watchID},
		CreatedAt:       h.clock.at,
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (h *w9Harness) pickIncident(index int) *BlockerIncident {
	ids := make([]string, 0, len(h.incidents))
	for id := range h.incidents {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return h.incidents[ids[index%len(ids)]]
}

func (h *w9Harness) pickWatch(index int) (Watch, bool) {
	all, _ := h.store.AllWatches()
	if len(all) == 0 {
		return Watch{}, false
	}
	return all[index%len(all)], true
}

func (h *w9Harness) saveIfChanged(before, after Watch) {
	if !reflect.DeepEqual(before, after) {
		_ = h.store.SaveWatch(after)
	}
}

// recordDelivery models the wake ladder: posted and indeterminate end it; only
// a transport failure may retry, at most three attempts per obligation.
func (h *w9Harness) recordDelivery(key string, outcome AttemptOutcome, at time.Time) {
	attempts := h.deliveries[key]
	if len(attempts) > 0 {
		last := attempts[len(attempts)-1].Outcome
		if last == OutcomePosted || last == OutcomeIndeterminate || len(attempts) >= 3 {
			return
		}
	}
	attempts = append(attempts, Attempt{
		N: len(attempts) + 1, At: at, Channel: "w9", Outcome: outcome,
	})
	h.deliveries[key] = attempts
}

// ---- the ten checks -------------------------------------------------------

type w9Check func(*w9Harness) error

// 1. At most one active Watch per (repo, pr, headSHA).
func w9OneActiveWatch(h *w9Harness) error {
	active, err := h.store.ActiveWatches()
	if err != nil {
		return err
	}
	seen := make(map[w9Alias]string, len(active))
	for _, w := range active {
		a := w9Alias{repo: w.Repo, pr: w.PR, sha: w.HeadSHA}
		if prev, ok := seen[a]; ok {
			return fmt.Errorf("two active watches for %+v: %s and %s", a, prev, w.ID)
		}
		seen[a] = w.ID
	}
	return nil
}

// 2. Revision never decreases and no value is reused for one Watch instance.
func w9RevisionMonotonic(h *w9Harness) error {
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

// 3. A cancelled Watch stays closed with its verdict: it never becomes ready.
func w9CancelledNotReady(h *w9Harness) error {
	if h.cancelWakes != 0 {
		return fmt.Errorf("cancel staged %d wake effects", h.cancelWakes)
	}
	for key, verdictAtCancel := range h.cancelled {
		w, err := h.store.Watch(key.id)
		if err != nil || keyOf(w) != key {
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

// 4. The response attached to one obligationID never changes after creation.
func w9ObligationStable(h *w9Harness) error {
	for id, want := range h.known {
		o, err := h.store.Obligation(id)
		if err != nil {
			return fmt.Errorf("obligation %s disappeared: %v", id, err)
		}
		if got := identityOf(o); got != want {
			return fmt.Errorf("obligation %s response changed: got %+v, want %+v", id, got, want)
		}
	}
	return nil
}

// 5. An indeterminate delivery is never retried.
func w9IndeterminateNoRetry(h *w9Harness) error {
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

// 6. Read-only Store calls never change state.
func w9ReadsDoNotWrite(h *w9Harness) error {
	before := h.store.snapshot()
	h.applyReads()
	after := h.store.snapshot()
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("read methods changed store: writes %d -> %d", before.writes, after.writes)
	}
	return nil
}

// 7. RoleFixer is accepted in addressing: it is a valid role, a fixer can be
// registered as a subscriber, and its obligation is keyed to it.
func w9FixerAddressing(h *w9Harness) error {
	if !RoleFixer.Valid() {
		return fmt.Errorf("%s is not a valid role", RoleFixer)
	}
	if got, ok := ParseRole("fixer"); !ok || got != RoleFixer {
		return fmt.Errorf("ParseRole(fixer) = %q, %v", got, ok)
	}
	declared := false
	for _, r := range ValidRoles() {
		if r == RoleFixer {
			declared = true
		}
	}
	if !declared {
		return fmt.Errorf("ValidRoles does not contain %s", RoleFixer)
	}
	for _, reg := range h.fixerRegs {
		w, err := h.store.Watch(reg.watchID)
		if err != nil {
			continue
		}
		found := false
		for _, sub := range w.Subscribers {
			if sub.ID == reg.subID {
				found = true
				if sub.Role != RoleFixer {
					return fmt.Errorf("fixer %s registered as %s", reg.subID, sub.Role)
				}
			}
		}
		if !found {
			return fmt.Errorf("fixer %s missing from watch %s", reg.subID, reg.watchID)
		}
		key := ObligationKey(reg.watchID, reg.revision, reg.subID)
		o, err := h.store.Obligation(key)
		if err != nil || o.SubscriberID != reg.subID || o.Revision != reg.revision {
			return fmt.Errorf("fixer obligation %s missing or wrong: %+v, %v", key, o, err)
		}
	}
	return nil
}

// 8. Resolving a BlockerIncident unblocks every linked Watch, with no wake.
func w9BlockerUnblocksAll(h *w9Harness) error {
	if h.structureViolation != "" {
		return fmt.Errorf("%s", h.structureViolation)
	}
	for _, exp := range h.unblockExpectations {
		w, err := h.store.Watch(exp.watchID)
		if err != nil {
			return fmt.Errorf("linked watch %s disappeared: %v", exp.watchID, err)
		}
		if w.Status != StatusWatching || w.Verdict != VerdictWatching {
			return fmt.Errorf("linked watch %s still %s/%s after resolve", exp.watchID, w.Status, w.Verdict)
		}
		if w.Blocked != nil {
			return fmt.Errorf("linked watch %s still carries Blocked info", exp.watchID)
		}
		if w.Revision != exp.revision {
			return fmt.Errorf("linked watch %s revision = %d, want %d", exp.watchID, w.Revision, exp.revision)
		}
	}
	return nil
}

// 9. A terminal verdict never sends a wake, in the state machine or in the CI
// verdict engine.
func w9TerminalNoWake(h *w9Harness) error {
	for _, eff := range h.terminalWakes {
		return fmt.Errorf("terminal verdict staged a wake: %+v", eff)
	}
	for _, c := range h.evalCases {
		if c.verdict != VerdictMerged && c.verdict != VerdictClosed {
			return fmt.Errorf("state %s produced verdict %s", c.state, c.verdict)
		}
		if c.wakeLen != 0 {
			return fmt.Errorf("terminal state %s produced %d wake targets", c.state, c.wakeLen)
		}
	}
	return nil
}

// 10. A repeated registration of the same Watch is idempotent.
func w9RegisterIdempotent(h *w9Harness) error {
	if h.registerViolation != "" {
		return fmt.Errorf("%s", h.registerViolation)
	}
	return nil
}

func TestW9EventInvariants(t *testing.T) {
	checks := []struct {
		name  string
		check w9Check
	}{
		{"w9_one_active_watch", w9OneActiveWatch},
		{"w9_revision_monotonic", w9RevisionMonotonic},
		{"w9_cancelled_not_ready", w9CancelledNotReady},
		{"w9_obligation_stable", w9ObligationStable},
		{"w9_indeterminate_no_retry", w9IndeterminateNoRetry},
		{"w9_reads_do_not_write", w9ReadsDoNotWrite},
		{"w9_fixer_addressing", w9FixerAddressing},
		{"w9_blocker_unblocks_all", w9BlockerUnblocksAll},
		{"w9_terminal_no_wake", w9TerminalNoWake},
		{"w9_register_idempotent", w9RegisterIdempotent},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			runInvariantProperty(t, func(rt *rapid.T) {
				h := newW9Harness()
				seq := w9EventSeqGen.Draw(rt, "events")
				for _, e := range seq {
					h.apply(e)
					if err := tc.check(h); err != nil {
						rt.Fatalf(
							"W9 invariant %s violated: %v\nbase seed=%d (rapid prints the seed to reproduce)\nsequence (%d events): %s",
							tc.name, err, invariantSeed, len(seq), shortenTrace(h.trace),
						)
					}
				}
			})
		})
	}
}
