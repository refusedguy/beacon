package wake

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrNoPaneCapturer means verification was asked to run without a capturer.
var ErrNoPaneCapturer = errors.New("wake: no pane capturer for input verification")

// InputBoxState is what one input-box check observed.
type InputBoxState string

const (
	// InputReady means the pane shows an input box with a visible cursor and
	// no leftover draft, so a message can safely be typed into it.
	InputReady InputBoxState = "ready"
	// InputNotReady means the pane has no identifiable input box, no visible
	// cursor, or still holds text in the box. A queued message must not be
	// reported as delivered in this state.
	InputNotReady InputBoxState = "not_ready"
)

var inputCursorMarkers = [...]string{"▌", "█", "▏", "❚"}

var inputPromptMarkers = [...]string{"❯", "›", "»", "⏵", "➜", ">"}

// VerifyInputBox checks the target pane through capture and reports whether
// its input box is ready. capture returns the visible text of one pane; the
// tmux implementation belongs to the pane-capture layer (CapturePane), so this
// file never issues capture-pane itself. want is the exact text expected in the
// box; pass an empty string to require an empty box, which is the state after
// Enter or a clean prompt before typing. A non-empty want must match a whole
// word, so "ship" is not accepted as part of "relationship".
func VerifyInputBox(ctx context.Context, capture func(context.Context, string) (string, error), target, want string) (InputBoxState, error) {
	if capture == nil {
		return InputNotReady, ErrNoPaneCapturer
	}
	frame, err := capture(ctx, target)
	if err != nil {
		return InputNotReady, err
	}
	return classifyInputFrame(frame, want), nil
}

// classifyInputFrame inspects the bottom-most prompt line of a snapshot. It is
// pure so ready and not-ready cases are testable without a live tmux pane.
func classifyInputFrame(frame, want string) InputBoxState {
	line, ok := inputBoxLine(frame)
	if !ok || !hasInputCursor(line) {
		return InputNotReady
	}
	box := inputBoxText(line)
	if want == "" {
		if box != "" {
			return InputNotReady
		}
		return InputReady
	}
	if !boxHasWord(box, want) {
		return InputNotReady
	}
	return InputReady
}

// inputBoxLine returns the lowest line that carries an input prompt. Some TUI
// renderers show only the cursor as the input affordance, so a cursor-only line
// is the fallback.
func inputBoxLine(frame string) (string, bool) {
	lines := strings.Split(frame, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := historyANSI.ReplaceAllString(lines[i], "")
		for _, marker := range inputPromptMarkers {
			if strings.Contains(line, marker) {
				return line, true
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := historyANSI.ReplaceAllString(lines[i], "")
		if hasInputCursor(line) {
			return line, true
		}
	}
	return "", false
}

func hasInputCursor(line string) bool {
	for _, marker := range inputCursorMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// inputBoxText is the text after the last prompt marker, with cursor glyphs
// and ANSI styling removed.
func inputBoxText(line string) string {
	line = historyANSI.ReplaceAllString(line, "")
	for _, marker := range inputCursorMarkers {
		line = strings.ReplaceAll(line, marker, "")
	}
	cut := -1
	for _, marker := range inputPromptMarkers {
		if i := strings.LastIndex(line, marker); i >= 0 && i+len(marker) > cut {
			cut = i + len(marker)
		}
	}
	if cut >= 0 {
		line = line[cut:]
	}
	return strings.TrimSpace(line)
}

// boxHasWord reports whether want occurs in box as a whole word.
func boxHasWord(box, want string) bool {
	if want == "" {
		return false
	}
	for from := 0; from <= len(box); {
		i := strings.Index(box[from:], want)
		if i < 0 {
			return false
		}
		i += from
		before := i == 0
		if !before {
			r, _ := utf8.DecodeLastRuneInString(box[:i])
			before = !isWordRune(r)
		}
		after := i+len(want) == len(box)
		if !after {
			r, _ := utf8.DecodeRuneInString(box[i+len(want):])
			after = !isWordRune(r)
		}
		if before && after {
			return true
		}
		from = i + len(want)
	}
	return false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
