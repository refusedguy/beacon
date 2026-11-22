package domain

import (
	"reflect"
	"testing"
	"time"
)

func transitionWatch(status Status, verdict Verdict, headSHA string, revision uint64) Watch {
	return Watch{
		ID:          "w1",
		Repo:        "org/repo",
		PR:          7,
		HeadSHA:     headSHA,
		Status:      status,
		Verdict:     verdict,
		Revision:    revision,
		Subscribers: []Subscriber{{ID: "s1", Role: RoleExecutor}, {ID: "s2", Role: RoleOrchestrator}},
	}
}

func TestTransition(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	oldSHA, newSHA := "oldsha", "newsha"

	tests := []struct {
		name             string
		status           Status
		verdict          Verdict
		headSHA          string
		revision         uint64
		event            Event
		inputCloseReason CloseReason
		wantStatus       Status
		wantVerdict      Verdict
		wantRevision     uint64
		wantCloseReason  CloseReason
		wantClosedAt     time.Time
		wantEffectKind   EffectKind
		wantEffectHead   string
		wantEffectRev    uint64
		wantEffectVrdt   Verdict
		wantEffectSubs   int
	}{
		// watching -> watching: verdict watching repeated, no revision, no effect.
		{
			name: "watching to watching on repeated verdict", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      VerdictEvent(VerdictWatching, at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 3,
		},
		// watching -> notifying: first actionable verdict.
		{
			name: "watching to notifying on ready", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      VerdictEvent(VerdictReady, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictReady, wantRevision: 4,
			wantEffectKind: EffectWakeSubscribers, wantEffectRev: 4, wantEffectVrdt: VerdictReady,
		},
		{
			name: "watching to notifying on rebase", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 0,
			event:      VerdictEvent(VerdictRebase, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictRebase, wantRevision: 1,
			wantEffectKind: EffectWakeSubscribers, wantEffectRev: 1, wantEffectVrdt: VerdictRebase,
		},
		{
			name: "watching to notifying on retry", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 0,
			event:      VerdictEvent(VerdictRetry, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictRetry, wantRevision: 1,
			wantEffectKind: EffectWakeSubscribers, wantEffectRev: 1, wantEffectVrdt: VerdictRetry,
		},
		{
			name: "watching to notifying on keep_working", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 0,
			event:      VerdictEvent(VerdictKeepWorking, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictKeepWorking, wantRevision: 1,
			wantEffectKind: EffectWakeSubscribers, wantEffectRev: 1, wantEffectVrdt: VerdictKeepWorking,
		},
		{
			name: "watching to notifying on timeout", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 0,
			event:      VerdictEvent(VerdictTimeout, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictTimeout, wantRevision: 1,
			wantEffectKind: EffectWakeSubscribers, wantEffectRev: 1, wantEffectVrdt: VerdictTimeout,
		},
		// watching -> closed: terminal verdicts.
		{
			name: "watching to closed on merged", status: StatusWatching, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictMerged, at),
			wantStatus: StatusClosed, wantVerdict: VerdictMerged, wantRevision: 5,
			wantCloseReason: CloseReasonMerged, wantClosedAt: at,
		},
		{
			name: "watching to closed on closed verdict", status: StatusWatching, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictClosed, at),
			wantStatus: StatusClosed, wantVerdict: VerdictClosed, wantRevision: 5,
			wantCloseReason: CloseReasonClosed, wantClosedAt: at,
		},
		// ADR 0059 §6: only a successful acceptance command closes the Watch.
		{
			name: "successful acceptance closes with reason accepted", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      AcceptEvent(true, at),
			wantStatus: StatusClosed, wantVerdict: VerdictAccepted, wantRevision: 5,
			wantCloseReason: CloseReasonAccepted, wantClosedAt: at,
		},
		{
			name: "failed acceptance leaves the Watch open", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 4,
			event:      AcceptEvent(false, at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 4,
		},
		// notifying -> notifying.
		{
			name: "notifying to notifying on repeated actionable verdict", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictReady, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictReady, wantRevision: 4,
		},
		{
			name: "notifying to notifying on new actionable verdict", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictRebase, at),
			wantStatus: StatusNotifying, wantVerdict: VerdictRebase, wantRevision: 5,
			wantEffectKind: EffectWakeSubscribers, wantEffectRev: 5, wantEffectVrdt: VerdictRebase,
		},
		// notifying -> watching: re-run downgrade bumps revision, wakes nobody.
		{
			name: "notifying to watching on rerun", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictWatching, at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 5,
		},
		{
			name: "notifying to watching on keep working rerun", status: StatusNotifying, verdict: VerdictKeepWorking, headSHA: oldSHA, revision: 6,
			event:      VerdictEvent(VerdictWatching, at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 7,
		},
		// notifying -> closed: terminal verdict.
		{
			name: "notifying to closed on merged", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictMerged, at),
			wantStatus: StatusClosed, wantVerdict: VerdictMerged, wantRevision: 5,
			wantCloseReason: CloseReasonMerged, wantClosedAt: at,
		},
		// cancel from any live state: closed, no effects.
		{
			name: "cancel from watching has no effects", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      CancelEvent(at),
			wantStatus: StatusClosed, wantVerdict: VerdictWatching, wantRevision: 3,
			wantCloseReason: CloseReasonCancelled, wantClosedAt: at,
		},
		{
			name: "cancel from notifying has no effects", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      CancelEvent(at),
			wantStatus: StatusClosed, wantVerdict: VerdictReady, wantRevision: 4,
			wantCloseReason: CloseReasonCancelled, wantClosedAt: at,
		},
		// head_changed: closes old watch and creates a new one for same subscribers.
		{
			name: "head changed from watching", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      HeadChangedEvent(newSHA, at),
			wantStatus: StatusClosed, wantVerdict: VerdictWatching, wantRevision: 3,
			wantCloseReason: CloseReasonHeadChanged, wantClosedAt: at,
			wantEffectKind: EffectCreateWatch, wantEffectHead: newSHA, wantEffectSubs: 2,
		},
		{
			name: "head changed from notifying", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      HeadChangedEvent(newSHA, at),
			wantStatus: StatusClosed, wantVerdict: VerdictReady, wantRevision: 4,
			wantCloseReason: CloseReasonHeadChanged, wantClosedAt: at,
			wantEffectKind: EffectCreateWatch, wantEffectHead: newSHA, wantEffectSubs: 2,
		},
		{
			name: "head changed to same sha is a no-op", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      HeadChangedEvent(oldSHA, at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 3,
		},
		// closed -> closed: terminal, no transitions out. Cancelled never becomes ready.
		{
			name: "closed stays closed on ready", status: StatusClosed, verdict: VerdictClosed, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictReady, at),
			wantStatus: StatusClosed, wantVerdict: VerdictClosed, wantRevision: 4,
		},
		{
			name: "cancelled stays closed on ready", status: StatusClosed, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:            VerdictEvent(VerdictReady, at),
			inputCloseReason: CloseReasonCancelled,
			wantStatus:       StatusClosed, wantVerdict: VerdictWatching, wantRevision: 3,
			wantCloseReason: CloseReasonCancelled,
		},
		{
			name: "closed stays closed on cancel", status: StatusClosed, verdict: VerdictMerged, headSHA: oldSHA, revision: 5,
			event:            CancelEvent(at),
			inputCloseReason: CloseReasonMerged,
			wantStatus:       StatusClosed, wantVerdict: VerdictMerged, wantRevision: 5,
			wantCloseReason: CloseReasonMerged,
		},
		{
			name: "closed stays closed on head changed", status: StatusClosed, verdict: VerdictMerged, headSHA: oldSHA, revision: 5,
			event:            HeadChangedEvent(newSHA, at),
			inputCloseReason: CloseReasonMerged,
			wantStatus:       StatusClosed, wantVerdict: VerdictMerged, wantRevision: 5,
			wantCloseReason: CloseReasonMerged,
		},
		// blocked: a block keeps the Watch open and wakes nobody; only an explicit
		// unblock returns it to watching.
		{
			name: "watching to blocked on block", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      BlockEvent(BlockedInfo{Reason: BlockReasonBaseChecksFailed}, at),
			wantStatus: StatusBlocked, wantVerdict: VerdictBlocked, wantRevision: 4,
		},
		{
			name: "notifying to blocked on block", status: StatusNotifying, verdict: VerdictReady, headSHA: oldSHA, revision: 4,
			event:      BlockEvent(BlockedInfo{Reason: BlockReasonBaseChecksFailed}, at),
			wantStatus: StatusBlocked, wantVerdict: VerdictBlocked, wantRevision: 5,
		},
		{
			name: "blocked stays blocked on repeated block", status: StatusBlocked, verdict: VerdictBlocked, headSHA: oldSHA, revision: 4,
			event:      BlockEvent(BlockedInfo{Reason: BlockReasonNotMine}, at),
			wantStatus: StatusBlocked, wantVerdict: VerdictBlocked, wantRevision: 4,
		},
		{
			name: "blocked ignores a verdict", status: StatusBlocked, verdict: VerdictBlocked, headSHA: oldSHA, revision: 4,
			event:      VerdictEvent(VerdictReady, at),
			wantStatus: StatusBlocked, wantVerdict: VerdictBlocked, wantRevision: 4,
		},
		{
			name: "blocked to watching on unblock", status: StatusBlocked, verdict: VerdictBlocked, headSHA: oldSHA, revision: 4,
			event:      UnblockEvent(at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 5,
		},
		{
			name: "unblock on non-blocked watch is a no-op", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      UnblockEvent(at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 3,
		},
		{
			name: "cancel from blocked has no effects", status: StatusBlocked, verdict: VerdictBlocked, headSHA: oldSHA, revision: 4,
			event:      CancelEvent(at),
			wantStatus: StatusClosed, wantVerdict: VerdictBlocked, wantRevision: 4,
			wantCloseReason: CloseReasonCancelled, wantClosedAt: at,
		},
		// Invalid input and unknown events are total no-ops.
		{
			name: "unknown verdict is a no-op", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      VerdictEvent(Verdict("bogus"), at),
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 3,
		},
		{
			name: "unknown event kind is a no-op", status: StatusWatching, verdict: VerdictWatching, headSHA: oldSHA, revision: 3,
			event:      Event{Kind: EventKind("tick")},
			wantStatus: StatusWatching, wantVerdict: VerdictWatching, wantRevision: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := transitionWatch(tc.status, tc.verdict, tc.headSHA, tc.revision)
			w.CloseReason = string(tc.inputCloseReason)
			got, effects := Transition(w, tc.event)

			if got.Status != tc.wantStatus {
				t.Errorf("status: got %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Verdict != tc.wantVerdict {
				t.Errorf("verdict: got %q, want %q", got.Verdict, tc.wantVerdict)
			}
			if got.Revision != tc.wantRevision {
				t.Errorf("revision: got %d, want %d", got.Revision, tc.wantRevision)
			}
			if got.CloseReason != string(tc.wantCloseReason) {
				t.Errorf("closeReason: got %q, want %q", got.CloseReason, tc.wantCloseReason)
			}
			if !got.ClosedAt.Equal(tc.wantClosedAt) {
				t.Errorf("closedAt: got %v, want %v", got.ClosedAt, tc.wantClosedAt)
			}

			if tc.wantEffectKind == "" {
				if len(effects) != 0 {
					t.Fatalf("effects: got %d (%+v), want 0", len(effects), effects)
				}
				return
			}
			if len(effects) != 1 {
				t.Fatalf("effects: got %d (%+v), want 1", len(effects), effects)
			}
			e := effects[0]
			if e.Kind != tc.wantEffectKind {
				t.Errorf("effect kind: got %q, want %q", e.Kind, tc.wantEffectKind)
			}
			if e.WatchID != "w1" {
				t.Errorf("effect watchID: got %q, want w1", e.WatchID)
			}
			if e.Revision != tc.wantEffectRev {
				t.Errorf("effect revision: got %d, want %d", e.Revision, tc.wantEffectRev)
			}
			if e.Verdict != tc.wantEffectVrdt {
				t.Errorf("effect verdict: got %q, want %q", e.Verdict, tc.wantEffectVrdt)
			}
			if e.HeadSHA != tc.wantEffectHead {
				t.Errorf("effect headSHA: got %q, want %q", e.HeadSHA, tc.wantEffectHead)
			}
			if len(e.Subscribers) != tc.wantEffectSubs {
				t.Errorf("effect subscribers: got %d, want %d", len(e.Subscribers), tc.wantEffectSubs)
			}
		})
	}
}

func TestTransitionWatchingToWatching(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 7)
	next, effects := Transition(w, VerdictEvent(VerdictWatching, at))

	if next.Revision != w.Revision {
		t.Errorf("watching->watching changed revision: got %d, want %d", next.Revision, w.Revision)
	}
	if len(effects) != 0 {
		t.Errorf("watching->watching produced effects: %+v", effects)
	}
}

func TestTransitionCancelNoEffects(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, v := range []Verdict{VerdictWatching, VerdictReady, VerdictTimeout} {
		w := transitionWatch(StatusWatching, v, "sha", 2)
		next, effects := Transition(w, CancelEvent(at))
		if len(effects) != 0 {
			t.Errorf("cancel with verdict %q produced effects: %+v", v, effects)
		}
		if next.Status != StatusClosed || next.CloseReason != string(CloseReasonCancelled) {
			t.Errorf("cancel with verdict %q: status=%q reason=%q", v, next.Status, next.CloseReason)
		}
	}
}

func TestTransitionDoesNotMutateInput(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 7)
	w.Checks = map[string]Check{"ci/test": {State: "COMPLETED"}}
	w.Flaky.Names = []string{"ci/flaky"}
	w.Flaky.Used = map[string]int{"ci/flaky": 1}
	before := cloneWatch(w)

	next, _ := Transition(w, VerdictEvent(VerdictReady, at))
	next.Status = StatusClosed
	next.Subscribers[0].ID = "mutated"
	next.Checks["ci/new"] = Check{}
	next.Flaky.Names[0] = "mutated"
	next.Flaky.Used["new"] = 1

	if !reflect.DeepEqual(w, before) {
		t.Fatalf("input Watch was mutated:\n got %+v\nwant %+v", w, before)
	}
}

// TestTransitionBlockDetails covers the block info carried on a Watch whose
// required checks fail and need outside action. Blocking wakes no subscriber,
// keeps its own copy of the details, is idempotent and is cleared on unblock.
func TestTransitionBlockDetails(t *testing.T) {
	at := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 7)
	before := cloneWatch(w)
	info := BlockedInfo{
		Reason:       BlockReasonBaseChecksFailed,
		FailedChecks: []string{"ci/test", "gates"},
		Since:        at,
		BlockedBy:    "ci-bridge",
	}

	blocked, effects := Transition(w, BlockEvent(info, at))
	if blocked.Status != StatusBlocked || blocked.Verdict != VerdictBlocked {
		t.Fatalf("block status/verdict = %q/%q, want %q/%q", blocked.Status, blocked.Verdict, StatusBlocked, VerdictBlocked)
	}
	if len(effects) != 0 {
		t.Fatalf("block staged effects (subscribers woken): %+v", effects)
	}
	if blocked.Blocked == nil {
		t.Fatal("block info is nil")
	}
	if !reflect.DeepEqual(*blocked.Blocked, info) {
		t.Errorf("block info = %+v, want %+v", *blocked.Blocked, info)
	}
	if blocked.Revision != w.Revision+1 {
		t.Errorf("block revision = %d, want %d", blocked.Revision, w.Revision+1)
	}
	if !reflect.DeepEqual(w, before) {
		t.Errorf("block mutated input Watch: got %+v, want %+v", w, before)
	}

	// The details are copied from the event. Mutating the caller's slice must
	// not change the Watch or the input.
	info.FailedChecks[0] = "mutated"
	if blocked.Blocked.FailedChecks[0] != "ci/test" {
		t.Errorf("block shares FailedChecks with the event: %+v", blocked.Blocked.FailedChecks)
	}
	blocked.Blocked.FailedChecks[1] = "mutated"
	if !reflect.DeepEqual(w, before) {
		t.Errorf("mutating block info changed the input Watch: %+v", w)
	}

	// A repeated block is idempotent and keeps the original details.
	again, effects := Transition(blocked, BlockEvent(BlockedInfo{Reason: BlockReasonNotMine}, at))
	if again.Revision != blocked.Revision {
		t.Errorf("repeated block changed revision: %d -> %d", blocked.Revision, again.Revision)
	}
	if again.Blocked == nil || again.Blocked.Reason != BlockReasonBaseChecksFailed {
		t.Errorf("repeated block replaced details: %+v", again.Blocked)
	}
	if len(effects) != 0 {
		t.Errorf("repeated block staged effects: %+v", effects)
	}

	// A block without details is ignored.
	if got, eff := Transition(w, Event{Kind: EventBlock, At: at}); got.Blocked != nil || got.Revision != w.Revision || len(eff) != 0 {
		t.Errorf("block without details changed the Watch: %+v, %+v", got, eff)
	}

	// Unblock resumes watching, clears the details, bumps the revision and
	// wakes nobody.
	resumed, effects := Transition(blocked, UnblockEvent(at))
	if resumed.Status != StatusWatching || resumed.Verdict != VerdictWatching {
		t.Errorf("unblock status/verdict = %q/%q, want watching/watching", resumed.Status, resumed.Verdict)
	}
	if resumed.Blocked != nil {
		t.Errorf("unblock kept block details: %+v", resumed.Blocked)
	}
	if resumed.Revision != blocked.Revision+1 {
		t.Errorf("unblock revision = %d, want %d", resumed.Revision, blocked.Revision+1)
	}
	if len(effects) != 0 {
		t.Errorf("unblock staged effects: %+v", effects)
	}
}

func TestTransitionDeterministic(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusNotifying, VerdictReady, "sha", 4)
	e := VerdictEvent(VerdictRebase, at)

	a, ea := Transition(w, e)
	b, eb := Transition(w, e)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ea, eb) {
		t.Fatalf("Transition is not deterministic:\n(%+v, %+v)\n(%+v, %+v)", a, ea, b, eb)
	}
}

// TestTransitionNeedsInputParksWatch covers T3.21: an agent question parks the
// Watch without closing it. A second question is idempotent.
func TestTransitionNeedsInputParksWatch(t *testing.T) {
	at := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 3)

	parked, effects := Transition(w, NeedsInputEvent(at))
	if parked.Status != StatusNeedsInput {
		t.Errorf("status = %q, want %q", parked.Status, StatusNeedsInput)
	}
	if parked.Verdict != VerdictNeedsInput {
		t.Errorf("verdict = %q, want %q", parked.Verdict, VerdictNeedsInput)
	}
	if parked.Revision != 4 {
		t.Errorf("revision = %d, want 4", parked.Revision)
	}
	if parked.ID != w.ID || !reflect.DeepEqual(parked.Subscribers, w.Subscribers) {
		t.Errorf("parking changed the task identity: %+v", parked)
	}
	if len(effects) != 0 {
		t.Errorf("parking produced effects %+v, want none", effects)
	}
	if !parked.ClosedAt.IsZero() || parked.CloseReason != "" {
		t.Errorf("parking closed the Watch: reason %q at %v", parked.CloseReason, parked.ClosedAt)
	}

	again, effects := Transition(parked, NeedsInputEvent(at.Add(time.Minute)))
	if !reflect.DeepEqual(again, parked) || len(effects) != 0 {
		t.Errorf("repeated question changed the Watch: %+v, effects %+v", again, effects)
	}
	if w.Status != StatusWatching || w.Revision != 3 {
		t.Errorf("Transition mutated its input: %+v", w)
	}
}

// TestTransitionContinueResumesSameWatch proves the human answer resumes the
// parked Watch and emits no successor Watch (T3.21 acceptance: no new task).
func TestTransitionContinueResumesSameWatch(t *testing.T) {
	at := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	parked := transitionWatch(StatusNeedsInput, VerdictNeedsInput, "sha", 4)

	resumed, effects := Transition(parked, ContinueEvent("w1", 1, at))
	if resumed.Status != StatusWatching {
		t.Errorf("status = %q, want %q", resumed.Status, StatusWatching)
	}
	if resumed.Verdict != VerdictWatching {
		t.Errorf("verdict = %q, want %q", resumed.Verdict, VerdictWatching)
	}
	if resumed.ID != "w1" || resumed.Revision != 5 {
		t.Errorf("resumed Watch = %+v, want w1 at revision 5", resumed)
	}
	if len(effects) != 1 {
		t.Fatalf("effects = %+v, want one continue effect", effects)
	}
	if effects[0].Kind != EffectContinueWatch || effects[0].WatchID != "w1" || effects[0].Revision != 5 {
		t.Errorf("effect = %+v, want continue_watch on w1 at revision 5", effects[0])
	}
	for _, effect := range effects {
		if effect.Kind == EffectCreateWatch {
			t.Error("a human answer created a new Watch")
		}
	}
	if parked.Status != StatusNeedsInput || parked.Revision != 4 {
		t.Errorf("Transition mutated its input: %+v", parked)
	}
}

// TestTransitionContinueRoundLimit enforces the ADR 0031 §3 cap of five answer
// rounds. A sixth answer is a no-op and does not create a Watch.
func TestTransitionContinueRoundLimit(t *testing.T) {
	at := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	for round := uint64(1); round <= MaxContinueRounds+1; round++ {
		t.Run(string(rune('0'+round)), func(t *testing.T) {
			parked := transitionWatch(StatusNeedsInput, VerdictNeedsInput, "sha", round*2)
			next, effects := Transition(parked, ContinueEvent("w1", round, at))
			if round <= MaxContinueRounds {
				if !CanContinueRound(round) {
					t.Errorf("CanContinueRound(%d) = false, want true", round)
				}
				if next.Status != StatusWatching || next.Revision != parked.Revision+1 {
					t.Errorf("round %d: next = %+v, want watching at %d", round, next, parked.Revision+1)
				}
				return
			}
			if CanContinueRound(round) {
				t.Errorf("CanContinueRound(%d) = true, want false", round)
			}
			if next.Status != StatusNeedsInput || next.Revision != parked.Revision || len(effects) != 0 {
				t.Errorf("round %d was accepted: %+v, effects %+v", round, next, effects)
			}
		})
	}
	if CanContinueRound(0) {
		t.Error("round zero is not a valid answer round")
	}
}

// TestTransitionContinueRequiresSameWatch rejects an answer that names a
// different Watch: there is no path from continue_of to a new task.
func TestTransitionContinueRequiresSameWatch(t *testing.T) {
	at := time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)
	parked := transitionWatch(StatusNeedsInput, VerdictNeedsInput, "sha", 4)
	for _, e := range []Event{
		ContinueEvent("other", 1, at),
		ContinueEvent("w1", 0, at),
		ContinueEvent("w1", MaxContinueRounds+1, at),
	} {
		next, effects := Transition(parked, e)
		if !reflect.DeepEqual(next, parked) || len(effects) != 0 {
			t.Errorf("event %+v resumed the Watch: %+v, effects %+v", e, next, effects)
		}
	}

	// A human answer cannot revive a closed Watch either.
	closed := transitionWatch(StatusClosed, VerdictMerged, "sha", 5)
	next, effects := Transition(closed, ContinueEvent("w1", 1, at))
	if !reflect.DeepEqual(next, closed) || len(effects) != 0 {
		t.Errorf("continue revived a closed Watch: %+v, effects %+v", next, effects)
	}
}

// TestTransitionParkOnFailure covers T3.60: a failed execution step parks the
// Watch and carries the full error text and reason to the journal. The only
// effect is a journal entry, so no wake, continue or successor is staged.
func TestTransitionParkOnFailure(t *testing.T) {
	at := time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC)
	fullError := "limit exceeded: 429\ndetail: quota=999999\nstack: line 42\n<raw & unescaped>"

	for _, reason := range []ParkReason{ParkReasonLimit, ParkReasonBudget, ParkReasonCrash, ParkReasonError} {
		t.Run(string(reason), func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				status  Status
				verdict Verdict
			}{
				{"from watching", StatusWatching, VerdictWatching},
				{"from notifying", StatusNotifying, VerdictReady},
				{"from needs_input", StatusNeedsInput, VerdictNeedsInput},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := transitionWatch(tc.status, tc.verdict, "sha", 3)
					before := cloneWatch(w)
					next, effects := Transition(w, ParkEvent(reason, fullError, at))

					if next.Status != StatusParked {
						t.Errorf("status = %q, want %q", next.Status, StatusParked)
					}
					if next.Verdict != VerdictParked {
						t.Errorf("verdict = %q, want %q", next.Verdict, VerdictParked)
					}
					if next.Revision != 4 {
						t.Errorf("revision = %d, want 4", next.Revision)
					}
					if next.ID != w.ID || !reflect.DeepEqual(next.Subscribers, w.Subscribers) {
						t.Errorf("parking changed the task identity: %+v", next)
					}
					if !next.ClosedAt.IsZero() || next.CloseReason != "" {
						t.Errorf("parking closed the Watch: reason %q at %v", next.CloseReason, next.ClosedAt)
					}
					if len(effects) != 1 {
						t.Fatalf("effects = %+v, want exactly one journal effect", effects)
					}
					e := effects[0]
					if e.Kind != EffectLogPark {
						t.Errorf("effect kind = %q, want %q", e.Kind, EffectLogPark)
					}
					if e.WatchID != w.ID || e.Revision != 4 {
						t.Errorf("effect = %+v, want w1 at revision 4", e)
					}
					if e.ParkReason != reason {
						t.Errorf("effect reason = %q, want %q", e.ParkReason, reason)
					}
					if e.ParkError != fullError {
						t.Errorf("effect error = %q, want full text %q", e.ParkError, fullError)
					}
					for _, eff := range effects {
						switch eff.Kind {
						case EffectWakeSubscribers, EffectContinueWatch, EffectCreateWatch:
							t.Errorf("parking staged a retry effect: %+v", eff)
						}
					}
					if !reflect.DeepEqual(w, before) {
						t.Errorf("Transition mutated its input: %+v", w)
					}
				})
			}
		})
	}
}

// TestTransitionParkIsIdempotent proves a repeated failure does not bump the
// revision or write a second journal entry, and leaves the input untouched.
func TestTransitionParkIsIdempotent(t *testing.T) {
	at := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 3)
	event := ParkEvent(ParkReasonLimit, "first failure", at)

	parked, effects := Transition(w, event)
	again, effectsAgain := Transition(parked, ParkEvent(ParkReasonError, "second failure", at.Add(time.Minute)))

	if !reflect.DeepEqual(again, parked) {
		t.Errorf("re-park changed the Watch: %+v, want %+v", again, parked)
	}
	if len(effectsAgain) != 0 {
		t.Errorf("re-park emitted journal entries: %+v", effectsAgain)
	}
	if len(effects) != 1 {
		t.Fatalf("first park emitted %d effects, want 1", len(effects))
	}
	if w.Status != StatusWatching || w.Revision != 3 {
		t.Errorf("Transition mutated its input: %+v", w)
	}
}

// TestTransitionParkedHasNoAutomaticRetry covers the T3.60 acceptance: a
// parked Watch never resumes from a verdict, a human-answer event or a
// needs_input event. The failed step is not retried by any of them.
func TestTransitionParkedHasNoAutomaticRetry(t *testing.T) {
	at := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	parked := transitionWatch(StatusParked, VerdictParked, "sha", 4)

	events := []struct {
		name string
		e    Event
	}{
		{"actionable verdict", VerdictEvent(VerdictReady, at)},
		{"watching verdict", VerdictEvent(VerdictWatching, at)},
		{"terminal verdict", VerdictEvent(VerdictMerged, at)},
		{"continue with valid round", ContinueEvent("w1", 1, at)},
		{"needs input", NeedsInputEvent(at)},
	}
	for _, tc := range events {
		t.Run(tc.name, func(t *testing.T) {
			next, effects := Transition(parked, tc.e)
			if !reflect.DeepEqual(next, parked) {
				t.Errorf("event %+v changed the parked Watch: %+v", tc.e, next)
			}
			if len(effects) != 0 {
				t.Errorf("event %+v staged effects: %+v", tc.e, effects)
			}
		})
	}
}

// TestTransitionClosedCannotBeParked proves parking never reopens a closed Watch.
func TestTransitionClosedCannotBeParked(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	closed := transitionWatch(StatusClosed, VerdictMerged, "sha", 5)
	closed.CloseReason = string(CloseReasonMerged)
	closed.ClosedAt = at

	next, effects := Transition(closed, ParkEvent(ParkReasonCrash, "crash text", at))
	if !reflect.DeepEqual(next, closed) || len(effects) != 0 {
		t.Errorf("parking revived a closed Watch: %+v, effects %+v", next, effects)
	}
}

// TestTransitionParkedTerminalEvents documents that cancel and head_changed
// still close a parked Watch. Cancel is an explicit human action; head change
// is the ADR 0019 response to a new PR head, not a retry of the failed step.
// Neither touches the ADR 0017 delivery ladder.
func TestTransitionParkedTerminalEvents(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	parked := transitionWatch(StatusParked, VerdictParked, "oldsha", 4)

	cancelled, effects := Transition(parked, CancelEvent(at))
	if cancelled.Status != StatusClosed || cancelled.CloseReason != string(CloseReasonCancelled) {
		t.Errorf("cancel from parked: %+v", cancelled)
	}
	if len(effects) != 0 {
		t.Errorf("cancel from parked produced effects: %+v", effects)
	}

	closed, effects := Transition(parked, HeadChangedEvent("newsha", at))
	if closed.Status != StatusClosed || closed.CloseReason != string(CloseReasonHeadChanged) {
		t.Errorf("head change from parked: %+v", closed)
	}
	if len(effects) != 1 || effects[0].Kind != EffectCreateWatch {
		t.Errorf("head change from parked effects: %+v", effects)
	}
	if parked.Status != StatusParked || parked.Revision != 4 {
		t.Errorf("Transition mutated its input: %+v", parked)
	}
}

// TestTransitionAcceptClosesOnSuccess covers T3.139: a successful explicit
// acceptance command is the close. The Watch keeps its identity and subscribers,
// carries the accepted verdict and reason, and stages no effect.
func TestTransitionAcceptClosesOnSuccess(t *testing.T) {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusNotifying, VerdictReady, "sha", 7)
	before := cloneWatch(w)

	next, effects := Transition(w, AcceptEvent(true, at))

	if next.Status != StatusClosed {
		t.Errorf("status = %q, want %q", next.Status, StatusClosed)
	}
	if next.CloseReason != string(CloseReasonAccepted) {
		t.Errorf("closeReason = %q, want %q", next.CloseReason, CloseReasonAccepted)
	}
	if next.Verdict != VerdictAccepted {
		t.Errorf("verdict = %q, want %q", next.Verdict, VerdictAccepted)
	}
	if next.Revision != 8 {
		t.Errorf("revision = %d, want 8", next.Revision)
	}
	if !next.ClosedAt.Equal(at) {
		t.Errorf("closedAt = %v, want %v", next.ClosedAt, at)
	}
	if next.ID != w.ID || !reflect.DeepEqual(next.Subscribers, w.Subscribers) {
		t.Errorf("acceptance changed task identity: %+v", next)
	}
	if len(effects) != 0 {
		t.Errorf("acceptance staged effects %+v, want none", effects)
	}
	if !reflect.DeepEqual(w, before) {
		t.Errorf("Transition mutated its input: %+v", w)
	}
}

// TestTransitionAcceptFromEveryLiveStatus proves acceptance closes a task from
// each live status, including needs_input and parked. It is an explicit human
// command, not a retry of a failed step or a resumption of a parked question.
func TestTransitionAcceptFromEveryLiveStatus(t *testing.T) {
	at := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status  Status
		verdict Verdict
	}{
		{StatusWatching, VerdictWatching},
		{StatusNotifying, VerdictReady},
		{StatusNeedsInput, VerdictNeedsInput},
		{StatusParked, VerdictParked},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			w := transitionWatch(tc.status, tc.verdict, "sha", 3)
			next, effects := Transition(w, AcceptEvent(true, at))
			if next.Status != StatusClosed || next.CloseReason != string(CloseReasonAccepted) || next.Verdict != VerdictAccepted {
				t.Errorf("acceptance from %s: next = %+v", tc.status, next)
			}
			if next.Revision != 4 || !next.ClosedAt.Equal(at) {
				t.Errorf("acceptance from %s: revision = %d closedAt = %v", tc.status, next.Revision, next.ClosedAt)
			}
			if len(effects) != 0 {
				t.Errorf("acceptance from %s staged effects %+v", tc.status, effects)
			}
		})
	}
}

// TestTransitionAcceptFailureLeavesWatchOpen proves a failed acceptance command
// is a no-op: the task stays live and no receipt or revision is produced.
func TestTransitionAcceptFailureLeavesWatchOpen(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status  Status
		verdict Verdict
	}{
		{StatusWatching, VerdictWatching},
		{StatusNotifying, VerdictReady},
		{StatusNeedsInput, VerdictNeedsInput},
		{StatusParked, VerdictParked},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			w := transitionWatch(tc.status, tc.verdict, "sha", 3)
			before := cloneWatch(w)
			next, effects := Transition(w, AcceptEvent(false, at))
			if !reflect.DeepEqual(next, before) || len(effects) != 0 {
				t.Errorf("failed acceptance changed the Watch: %+v, effects %+v", next, effects)
			}
		})
	}
}

// TestTransitionAcceptIsTerminal proves a closed Watch is never reopened or
// re-accepted: neither a second acceptance nor one on a differently closed
// Watch changes status, close reason or revision.
func TestTransitionAcceptIsTerminal(t *testing.T) {
	at := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 3)
	accepted, _ := Transition(w, AcceptEvent(true, at))

	again, effects := Transition(accepted, AcceptEvent(true, at.Add(time.Minute)))
	if !reflect.DeepEqual(again, accepted) || len(effects) != 0 {
		t.Errorf("second acceptance changed the Watch: %+v, effects %+v", again, effects)
	}

	closed := transitionWatch(StatusClosed, VerdictMerged, "sha", 5)
	closed.CloseReason = string(CloseReasonMerged)
	closed.ClosedAt = at
	next, effects := Transition(closed, AcceptEvent(true, at.Add(time.Minute)))
	if !reflect.DeepEqual(next, closed) || len(effects) != 0 {
		t.Errorf("acceptance reopened a closed Watch: %+v, effects %+v", next, effects)
	}

	failedWatch, failedEffects := Transition(closed, AcceptEvent(false, at))
	if !reflect.DeepEqual(failedWatch, closed) || len(failedEffects) != 0 {
		t.Errorf("failed acceptance changed a closed Watch: %+v, effects %+v", failedWatch, failedEffects)
	}
}

// TestTransitionDoneFlagAndExitDoNotClose is the ADR 0059 §6 guard: a done flag
// and a bare agent process exit are not the acceptance command. The verdict
// engine never emits VerdictAccepted, so a verdict event with that value is a
// no-op; an unknown exit event is a no-op too. Only AcceptEvent(true) closes.
func TestTransitionDoneFlagAndExitDoNotClose(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 3)

	if validVerdict(VerdictAccepted) {
		t.Error("VerdictAccepted is a valid verdict: a done flag could close a Watch")
	}

	for _, e := range []Event{
		VerdictEvent(VerdictAccepted, at),
		Event{Kind: EventKind("agent_exit"), At: at},
	} {
		next, effects := Transition(w, e)
		if !reflect.DeepEqual(next, w) || len(effects) != 0 {
			t.Errorf("event %+v closed the Watch: %+v, effects %+v", e, next, effects)
		}
	}
}

// TestBlockReasonValid covers the closed set: every defined code passes and
// neither the zero value nor a free-text sentence does (BCX-14).
func TestBlockReasonValid(t *testing.T) {
	for _, r := range []BlockReason{
		BlockReasonNotMine, BlockReasonResolved,
		BlockReasonBaseChecksFailed, BlockReasonRebaseBudgetExhausted,
	} {
		if !r.Valid() {
			t.Errorf("BlockReason(%q) rejected", r)
		}
	}
	for _, r := range []BlockReason{"", "not mine", "failed checks already present on base branch", "other"} {
		if r.Valid() {
			t.Errorf("BlockReason(%q) accepted", r)
		}
	}
}

// TestTransitionRejectsUnknownBlockReason proves the reason is checked on
// write: a free-text sentence cannot move a Watch into the blocked state.
func TestTransitionRejectsUnknownBlockReason(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	w := transitionWatch(StatusWatching, VerdictWatching, "sha", 3)
	for _, reason := range []BlockReason{"", "not mine", "failed checks already present on base branch"} {
		next, effects := Transition(w, BlockEvent(BlockedInfo{Reason: reason, Since: at}, at))
		if !reflect.DeepEqual(next, w) || len(effects) != 0 {
			t.Errorf("reason %q blocked the Watch: %+v, effects %+v", reason, next, effects)
		}
	}
}
