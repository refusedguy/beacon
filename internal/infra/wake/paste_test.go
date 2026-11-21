package wake

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// pasteFakeSender records both steps in one event log so the test can assert
// their order. Submit takes no text, so the fake cannot record any on submit.
type pasteFakeSender struct {
	events   *[]string
	pastes   []pasteFakeCall
	submits  []string
	pasteErr error
	subErr   error
}

type pasteFakeCall struct {
	target string
	text   string
}

func (f *pasteFakeSender) Paste(_ context.Context, target, text string) error {
	if f.events != nil {
		*f.events = append(*f.events, "paste")
	}
	f.pastes = append(f.pastes, pasteFakeCall{target: target, text: text})
	return f.pasteErr
}

func (f *pasteFakeSender) Submit(_ context.Context, target string) error {
	if f.events != nil {
		*f.events = append(*f.events, "submit")
	}
	f.submits = append(f.submits, target)
	return f.subErr
}

type pasteFakeGuard struct {
	matches bool
	err     error
	checks  int
	seen    []string
}

func (g *pasteFakeGuard) PaneMatches(_ context.Context, _, paneID string) (bool, error) {
	g.checks++
	g.seen = append(g.seen, paneID)
	return g.matches, g.err
}

func pasteTestInserter(sender PasteSender, guard PastePaneGuard) *PasteInserter {
	p := NewPasteInserter(sender, guard)
	p.Delay = func(context.Context) error { return nil }
	return p
}

func TestInsertTwoStepOrder(t *testing.T) {
	events := &[]string{}
	sender := &pasteFakeSender{events: events}
	p := pasteTestInserter(sender, nil)

	if err := p.InsertTwoStep(context.Background(), "pane-1", "ses_1", "hello"); err != nil {
		t.Fatalf("InsertTwoStep: %v", err)
	}
	wantEvents := []string{"paste", "submit"}
	if !reflect.DeepEqual(*events, wantEvents) {
		t.Fatalf("events = %v, want %v", *events, wantEvents)
	}
	if len(sender.pastes) != 1 || sender.pastes[0] != (pasteFakeCall{target: "pane-1", text: "hello"}) {
		t.Fatalf("pastes = %v, want one paste of hello to pane-1", sender.pastes)
	}
	if !reflect.DeepEqual(sender.submits, []string{"pane-1"}) {
		t.Fatalf("submits = %v, want [pane-1]", sender.submits)
	}
}

func TestInsertTwoStepDelayBetweenSteps(t *testing.T) {
	events := &[]string{}
	sender := &pasteFakeSender{events: events}
	p := NewPasteInserter(sender, nil)
	p.Delay = func(context.Context) error {
		*events = append(*events, "delay")
		return nil
	}

	if err := p.InsertTwoStep(context.Background(), "pane-1", "ses_1", "hello"); err != nil {
		t.Fatalf("InsertTwoStep: %v", err)
	}
	want := []string{"paste", "delay", "submit"}
	if !reflect.DeepEqual(*events, want) {
		t.Fatalf("events = %v, want %v", *events, want)
	}
}

func TestSubmitOnlyDoesNotAddText(t *testing.T) {
	sender := &pasteFakeSender{}
	p := pasteTestInserter(sender, nil)

	if err := p.SubmitOnly(context.Background(), "pane-1"); err != nil {
		t.Fatalf("SubmitOnly: %v", err)
	}
	if len(sender.pastes) != 0 {
		t.Fatalf("SubmitOnly wrote text: %v", sender.pastes)
	}
	if !reflect.DeepEqual(sender.submits, []string{"pane-1"}) {
		t.Fatalf("submits = %v, want [pane-1]", sender.submits)
	}

	if err := p.InsertTwoStep(context.Background(), "pane-1", "ses_1", "hello"); err != nil {
		t.Fatalf("InsertTwoStep: %v", err)
	}
	if err := p.SubmitOnly(context.Background(), "pane-1"); err != nil {
		t.Fatalf("second SubmitOnly: %v", err)
	}
	// The extra submit must not paste the buffered text a second time.
	if len(sender.pastes) != 1 || sender.pastes[0].text != "hello" {
		t.Fatalf("pastes = %v, want exactly one hello paste", sender.pastes)
	}
	if len(sender.submits) != 3 {
		t.Fatalf("submits = %v, want three submit-only steps", sender.submits)
	}
}

func TestInsertTwoStepMultilineTextStaysInBuffer(t *testing.T) {
	text := "first line\nsecond line\nthird line"
	sender := &pasteFakeSender{}
	p := pasteTestInserter(sender, nil)

	if err := p.InsertTwoStep(context.Background(), "pane-1", "ses_1", text); err != nil {
		t.Fatalf("InsertTwoStep: %v", err)
	}
	if len(sender.pastes) != 1 || sender.pastes[0].text != text {
		t.Fatalf("pastes = %v, want the whole multiline text", sender.pastes)
	}
	// The submit step has no text argument: newlines cannot become shell args
	// on a separate Enter call.
	if len(sender.submits) != 1 {
		t.Fatalf("submits = %v, want one submit", sender.submits)
	}
}

func TestInsertTwoStepPaneChangedWritesNothing(t *testing.T) {
	sender := &pasteFakeSender{}
	guard := &pasteFakeGuard{matches: false}
	p := pasteTestInserter(sender, guard)

	err := p.InsertTwoStep(context.Background(), "pane-1", "ses_new", "hello")
	if !errors.Is(err, ErrPaneChanged) {
		t.Fatalf("error = %v, want ErrPaneChanged", err)
	}
	if len(sender.pastes) != 0 {
		t.Fatalf("pane changed but text was pasted: %v", sender.pastes)
	}
	if len(sender.submits) != 0 {
		t.Fatalf("pane changed but Enter was sent: %v", sender.submits)
	}
	if guard.checks != 1 || !reflect.DeepEqual(guard.seen, []string{"ses_new"}) {
		t.Fatalf("guard checks = %d, seen = %v", guard.checks, guard.seen)
	}
}

func TestInsertTwoStepGuardErrorWritesNothing(t *testing.T) {
	wantErr := errors.New("pane layer down")
	sender := &pasteFakeSender{}
	p := pasteTestInserter(sender, &pasteFakeGuard{err: wantErr})

	err := p.InsertTwoStep(context.Background(), "pane-1", "ses_1", "hello")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped pane error", err)
	}
	if len(sender.pastes) != 0 || len(sender.submits) != 0 {
		t.Fatalf("guard error but steps ran: pastes=%v submits=%v", sender.pastes, sender.submits)
	}
}

func TestInsertTwoStepPasteErrorStopsSubmit(t *testing.T) {
	wantErr := errors.New("paste failed")
	sender := &pasteFakeSender{pasteErr: wantErr}
	p := pasteTestInserter(sender, nil)

	err := p.InsertTwoStep(context.Background(), "pane-1", "ses_1", "hello")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped paste error", err)
	}
	if len(sender.submits) != 0 {
		t.Fatalf("paste failed but Enter was sent: %v", sender.submits)
	}
}

func TestPasteInserterArgChecks(t *testing.T) {
	sender := &pasteFakeSender{}
	p := pasteTestInserter(sender, nil)

	if err := p.InsertTwoStep(context.Background(), "", "ses_1", "hello"); !errors.Is(err, ErrPasteNoTarget) {
		t.Fatalf("empty target error = %v, want ErrPasteNoTarget", err)
	}
	if err := p.SubmitOnly(context.Background(), ""); !errors.Is(err, ErrPasteNoTarget) {
		t.Fatalf("empty submit target error = %v, want ErrPasteNoTarget", err)
	}
	var nilInserter *PasteInserter
	if err := nilInserter.SubmitOnly(context.Background(), "pane-1"); !errors.Is(err, ErrPasteNoSender) {
		t.Fatalf("nil sender error = %v, want ErrPasteNoSender", err)
	}
	if len(sender.pastes) != 0 || len(sender.submits) != 0 {
		t.Fatal("arg checks reached the transport")
	}
}
