package wake

import (
	"context"
	"errors"
	"testing"
)

// fakeComposer is an in-memory Composer. initial simulates a pre-existing
// human draft; afterStage simulates late human input that changes the text
// after the wake was staged.
type fakeComposer struct {
	initial    string
	afterStage string
	text       string
	reads      int
	stages     []string
	commits    []string
}

func (f *fakeComposer) Text(context.Context, string) (string, error) {
	f.reads++
	if f.reads == 1 {
		return f.initial, nil
	}
	if f.afterStage != "" {
		return f.afterStage, nil
	}
	return f.text, nil
}

func (f *fakeComposer) Stage(_ context.Context, _ string, text string) error {
	f.text = text
	f.stages = append(f.stages, text)
	return nil
}

func (f *fakeComposer) Commit(_ context.Context, sessionID string) error {
	f.commits = append(f.commits, sessionID)
	return nil
}

func assertHeld(t *testing.T, err error, reason DraftRefusalReason, obligationID string) {
	t.Helper()
	var refusal *DraftRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %T %v, want *DraftRefusal", err, err)
	}
	if refusal.Reason != reason {
		t.Fatalf("reason = %q, want %q", refusal.Reason, reason)
	}
	if len(refusal.HeldIDs) != 1 || refusal.HeldIDs[0] != obligationID {
		t.Fatalf("held ids = %v, want [%s]", refusal.HeldIDs, obligationID)
	}
}

// An empty composer receives the exact Envelope, is read back after staging,
// and only then gets Enter. This is the draft-aware happy path.
func TestDraftAwareStageVerifyThenCommit(t *testing.T) {
	const sid = "ses_draft_ok"
	p := w2bPayload("draft-ok", sid, 1)
	c := &fakeComposer{}
	d := &DraftAwareDeliverer{Composer: c}

	if err := d.Deliver(context.Background(), sid, p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	want := Envelope(p)
	if c.reads != 2 {
		t.Fatalf("composer reads = %d, want 2 (before and after staging)", c.reads)
	}
	if len(c.stages) != 1 || c.stages[0] != want {
		t.Fatalf("staged = %q, want [%q]", c.stages, want)
	}
	if len(c.commits) != 1 || c.commits[0] != sid {
		t.Fatalf("commits = %v, want [%s]", c.commits, sid)
	}
}

// A human draft already in the composer blocks Enter. The wake is held with
// its original id, and nothing is staged into the foreign draft.
func TestDraftAwareBlocksForeignDraft(t *testing.T) {
	const sid = "ses_draft_foreign"
	p := w2bPayload("draft-foreign", sid, 1)
	c := &fakeComposer{initial: "human text"}
	d := &DraftAwareDeliverer{Composer: c}

	err := d.Deliver(context.Background(), sid, p)
	assertHeld(t, err, DraftReasonForeignDraft, p.ObligationID)
	if c.reads != 1 || len(c.stages) != 0 || len(c.commits) != 0 {
		t.Fatalf("foreign draft: reads=%d stages=%v commits=%v, want 1/0/0", c.reads, c.stages, c.commits)
	}
}

// Late or changed human input after staging makes the composer text diverge.
// Enter is blocked and the wake is still held.
func TestDraftAwareBlocksLateHumanInput(t *testing.T) {
	const sid = "ses_draft_late"
	p := w2bPayload("draft-late", sid, 1)
	c := &fakeComposer{afterStage: Envelope(p) + "late"}
	d := &DraftAwareDeliverer{Composer: c}

	err := d.Deliver(context.Background(), sid, p)
	assertHeld(t, err, DraftReasonTextChanged, p.ObligationID)
	if len(c.stages) != 1 || len(c.commits) != 0 {
		t.Fatalf("late input: stages=%v commits=%v, want one stage and no commit", c.stages, c.commits)
	}
}

// The draft-aware mode is opt-in: when it is set, the immediate PromptFunc and
// CLI transport are never touched.
func TestDraftAwareModeBypassesImmediatePath(t *testing.T) {
	const sid = "ses_draft_wired"
	p := w2bPayload("draft-wired", sid, 1)
	c := &fakeComposer{}
	immediateHit := false
	d := &OpenCodeDeliverer{
		PromptFunc: func(context.Context, string, string, string) error {
			immediateHit = true
			return nil
		},
		Runner: func(context.Context, ...string) ([]byte, error) {
			immediateHit = true
			return nil, nil
		},
		DraftAware: &DraftAwareDeliverer{Composer: c},
	}

	if err := d.Deliver(context.Background(), sid, p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if immediateHit {
		t.Fatal("immediate PromptFunc/CLI path ran in draft-aware mode")
	}
	if len(c.commits) != 1 {
		t.Fatalf("commits = %v, want one commit", c.commits)
	}
}

// The default path is unchanged: without DraftAware the whole Envelope goes
// through PromptFunc in one steer.
func TestDefaultPathStillUsesPromptFunc(t *testing.T) {
	const sid = "ses_draft_default"
	p := w2bPayload("draft-default", sid, 1)
	var gotText string
	d := &OpenCodeDeliverer{
		PromptFunc: func(_ context.Context, _, text, _ string) error {
			gotText = text
			return nil
		},
	}

	if err := d.Deliver(context.Background(), sid, p); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if gotText != Envelope(p) {
		t.Fatalf("prompt text = %q, want %q", gotText, Envelope(p))
	}
}
