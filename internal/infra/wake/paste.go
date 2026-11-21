package wake

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PasteSubmitDelay is the pause between loading the paste buffer and sending
// Enter. A terminal needs this long to move the buffered text into its input
// line before the submit key arrives.
const PasteSubmitDelay = 200 * time.Millisecond

var (
	// ErrPaneChanged means the target no longer holds the pane the text was
	// prepared for. InsertTwoStep returns it before writing any text, so a
	// stale pane never receives the message (T3.245).
	ErrPaneChanged = errors.New("wake: pane changed: text not written")
	// ErrPasteNoTarget means the insertion has no pane target to address.
	ErrPasteNoTarget = errors.New("wake: paste: empty pane target")
	// ErrPasteNoSender means the transport seam was never supplied.
	ErrPasteNoSender = errors.New("wake: paste: no sender")
)

// PasteSender is the transport seam for two-step insertion. The buffer call
// takes the whole text, including newlines, so a message never becomes shell
// arguments. The submit call carries no text: pressing Enter cannot resend the
// buffer or reinterpret it. A tmux adapter, a fake, or any other terminal can
// implement this interface; this package never calls tmux itself.
type PasteSender interface {
	// Paste writes text into target's paste buffer. It must not submit.
	Paste(ctx context.Context, target, text string) error
	// Submit sends Enter to target and nothing else.
	Submit(ctx context.Context, target string) error
}

// PastePaneGuard confirms that target still refers to the pane a buffer was
// prepared for. A changed pane returns false with a nil error. The pane layer
// that owns pane identity supplies this seam; wake only asks the question.
type PastePaneGuard interface {
	PaneMatches(ctx context.Context, target, paneID string) (bool, error)
}

// PasteInserter performs a two-step insertion: load the buffer, wait, then
// send Enter as a separate action. Keeping the two steps apart means the
// submit step has no text to add and a pane swap stops the insertion before
// any text is written.
type PasteInserter struct {
	Sender PasteSender
	Guard  PastePaneGuard
	// Delay waits between the buffer write and Enter. Tests inject a no-op;
	// production uses PasteSubmitDelay. A nil Delay uses the default.
	Delay func(ctx context.Context) error
}

// NewPasteInserter builds an inserter over the given transport and guard. The
// guard may be nil when no pane layer is present yet.
func NewPasteInserter(sender PasteSender, guard PastePaneGuard) *PasteInserter {
	return &PasteInserter{Sender: sender, Guard: guard}
}

// InsertTwoStep writes text into the registered pane, waits about 200 ms, and
// then submits with Enter. When a guard is configured it must confirm that
// target still holds paneID before the first byte is written. If the pane
// changed, nothing is pasted and nothing is submitted.
func (p *PasteInserter) InsertTwoStep(ctx context.Context, target, paneID, text string) error {
	if target == "" {
		return ErrPasteNoTarget
	}
	if p == nil || p.Sender == nil {
		return ErrPasteNoSender
	}
	if p.Guard != nil {
		match, err := p.Guard.PaneMatches(ctx, target, paneID)
		if err != nil {
			return fmt.Errorf("wake: check pane %s on %s: %w", paneID, target, err)
		}
		if !match {
			return ErrPaneChanged
		}
	}
	// Step one: the whole message goes into the buffer in one write. Passing
	// text here instead of on the submit call keeps newlines out of argv.
	if err := p.Sender.Paste(ctx, target, text); err != nil {
		return fmt.Errorf("wake: paste buffer on %s: %w", target, err)
	}
	if err := p.wait(ctx); err != nil {
		return fmt.Errorf("wake: paste delay on %s: %w", target, err)
	}
	// Step two: Enter only. No text is passed, so this step cannot add or
	// resend anything that was buffered.
	return p.SubmitOnly(ctx, target)
}

// SubmitOnly sends the submit step on its own. It carries no text by design,
// so a caller can commit a buffer that was prepared earlier without feeding
// the message in a second time.
func (p *PasteInserter) SubmitOnly(ctx context.Context, target string) error {
	if target == "" {
		return ErrPasteNoTarget
	}
	if p == nil || p.Sender == nil {
		return ErrPasteNoSender
	}
	if err := p.Sender.Submit(ctx, target); err != nil {
		return fmt.Errorf("wake: submit only on %s: %w", target, err)
	}
	return nil
}

// wait runs the injected delay or a timer for PasteSubmitDelay. A cancelled
// context stops the wait so a dead caller cannot park a wake.
func (p *PasteInserter) wait(ctx context.Context) error {
	if p.Delay != nil {
		return p.Delay(ctx)
	}
	timer := time.NewTimer(PasteSubmitDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
