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
