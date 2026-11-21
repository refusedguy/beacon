package followups

import (
	"errors"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{in: "", want: ModeOff},
		{in: "off", want: ModeOff},
		{in: "hold", want: ModeHold},
		{in: "draft-aware", want: ModeDraftAware},
		{in: "queue-only", want: ModeQueueOnly},
	}
	for _, tc := range cases {
		got, err := ParseMode(tc.in)
		if err != nil {
			t.Errorf("ParseMode(%q) returned error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if _, err := ParseMode("draft_aware"); !errors.Is(err, ErrUnknownMode) {
		t.Errorf("ParseMode(typo) error = %v, want ErrUnknownMode", err)
	}
}

func TestChooseActionAllModes(t *testing.T) {
	knownIdle := Signal{Known: true}
	knownTypingEmpty := Signal{Known: true, Typing: true}
	knownDraftNoTyping := Signal{Known: true, Draft: "my text"}
	knownTypingDraft := Signal{Known: true, Typing: true, Draft: "my text"}
	unknown := Signal{}

	cases := []struct {
		name string
		mode Mode
		sig  Signal
		want Action
	}{
		// off never guards, whatever the composer looks like.
		{"off/idle", ModeOff, knownIdle, ActionDeliver},
		{"off/typing", ModeOff, knownTypingEmpty, ActionDeliver},
		{"off/draft", ModeOff, knownDraftNoTyping, ActionDeliver},
		{"off/unknown", ModeOff, unknown, ActionDeliver},

		// hold keys on typing activity, plus the legacy fail safe.
		{"hold/idle", ModeHold, knownIdle, ActionDeliver},
		{"hold/typing", ModeHold, knownTypingEmpty, ActionHold},
		{"hold/draft-no-typing", ModeHold, knownDraftNoTyping, ActionDeliver},
		{"hold/typing-draft", ModeHold, knownTypingDraft, ActionHold},
		{"hold/unknown", ModeHold, unknown, ActionHold},

		// draft-aware keys on real content, not on the typing flag alone.
		{"draft-aware/idle", ModeDraftAware, knownIdle, ActionDeliver},
		{"draft-aware/typing-empty", ModeDraftAware, knownTypingEmpty, ActionDeliver},
		{"draft-aware/draft-no-typing", ModeDraftAware, knownDraftNoTyping, ActionHold},
		{"draft-aware/typing-draft", ModeDraftAware, knownTypingDraft, ActionHold},
		{"draft-aware/unknown", ModeDraftAware, unknown, ActionHold},

		// queue-only parks instead of holding, and a parked message is not lost.
		{"queue-only/idle", ModeQueueOnly, knownIdle, ActionDeliver},
		{"queue-only/typing", ModeQueueOnly, knownTypingEmpty, ActionQueue},
		{"queue-only/draft-no-typing", ModeQueueOnly, knownDraftNoTyping, ActionDeliver},
		{"queue-only/unknown", ModeQueueOnly, unknown, ActionQueue},

		// an unrecognised mode fails safe to hold.
		{"unknown-mode/idle", Mode("bogus"), knownIdle, ActionHold},
		{"unknown-mode/typing", Mode("bogus"), knownTypingEmpty, ActionHold},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ChooseAction(tc.mode, tc.sig); got != tc.want {
				t.Errorf("ChooseAction(%q, %+v) = %q, want %q", tc.mode, tc.sig, got, tc.want)
			}
		})
	}
}

func TestGuardDefaultOffAndSetMode(t *testing.T) {
	g := NewGuard()
	if got := g.Mode("s1"); got != ModeOff {
		t.Fatalf("default mode = %q, want %q", got, ModeOff)
	}
	if got := g.Plan("s1", Signal{Known: true, Typing: true}); got != ActionDeliver {
		t.Fatalf("default plan = %q, want %q", got, ActionDeliver)
	}

	if err := g.SetMode("s1", ModeHold); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got := g.Mode("s1"); got != ModeHold {
		t.Fatalf("mode after SetMode = %q, want %q", got, ModeHold)
	}
	if got := g.Plan("s1", Signal{Known: true, Typing: true}); got != ActionHold {
		t.Fatalf("plan after SetMode = %q, want %q", got, ActionHold)
	}

	events := g.Audit()
	if len(events) != 1 || events[0].Kind != AuditSetMode || events[0].Session != "s1" ||
		events[0].From != ModeOff || events[0].To != ModeHold {
		t.Fatalf("audit after SetMode = %+v, want one set-mode off->hold for s1", events)
	}
}

func TestGuardSetModeRejectsUnknown(t *testing.T) {
	g := NewGuard()
	if err := g.SetMode("s1", ModeHold); err != nil {
		t.Fatalf("SetMode hold: %v", err)
	}
	before := g.Audit()

	if err := g.SetMode("s1", Mode("bogus")); !errors.Is(err, ErrUnknownMode) {
		t.Fatalf("SetMode(bogus) error = %v, want ErrUnknownMode", err)
	}
	if got := g.Mode("s1"); got != ModeHold {
		t.Fatalf("mode after invalid SetMode = %q, want %q unchanged", got, ModeHold)
	}
	if after := g.Audit(); len(after) != len(before) {
		t.Fatalf("invalid SetMode added %d audit entries", len(after)-len(before))
	}
}

func TestGuardHoldViewRelease(t *testing.T) {
	g := NewGuard()
	first := g.Hold("alice", "wake one", ModeHold)
	_ = g.Hold("bob", "wake for bob", ModeQueueOnly)
	second := g.Hold("alice", "wake two", ModeDraftAware)

	view := g.View("alice")
	if len(view) != 2 || view[0].ID != first.ID || view[1].ID != second.ID {
		t.Fatalf("View(alice) = %+v, want %q then %q", view, first.ID, second.ID)
	}
	if view[0].Text != "wake one" || view[1].Text != "wake two" {
		t.Fatalf("View(alice) texts = %q, %q", view[0].Text, view[1].Text)
	}

	released, err := g.Release(first.ID)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.ID != first.ID || released.Text != "wake one" || released.Session != "alice" {
		t.Fatalf("Release returned %+v, want %+v", released, first)
	}
	if view = g.View("alice"); len(view) != 1 || view[0].ID != second.ID {
		t.Fatalf("View after Release = %+v, want only %q", view, second.ID)
	}
	if _, err := g.Release(first.ID); err == nil {
		t.Fatal("second Release of same id succeeded, want error")
	}
	if got := g.View("bob"); len(got) != 1 || got[0].Text != "wake for bob" {
		t.Fatalf("View(bob) = %+v, want the other session untouched", got)
	}

	events := g.Audit()
	if len(events) != 4 {
		t.Fatalf("audit length = %d, want 4 (three holds, one release)", len(events))
	}
	for i, kind := range []AuditKind{AuditHold, AuditHold, AuditHold, AuditRelease} {
		if events[i].Kind != kind {
			t.Errorf("audit[%d].Kind = %q, want %q", i, events[i].Kind, kind)
		}
	}
	if events[3].MessageID != first.ID {
		t.Fatalf("audit release id = %q, want %q", events[3].MessageID, first.ID)
	}
}

func TestGuardAuditIsCopy(t *testing.T) {
	g := NewGuard()
	if err := g.SetMode("s1", ModeHold); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	events := g.Audit()
	events[0].To = ModeOff
	if got := g.Audit()[0].To; got != ModeHold {
		t.Fatalf("mutating audit copy changed guard state to %q", got)
	}
}
