package wake

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// RenderStatus is what a pane capture proves about one wake envelope.
// rendered-unconfirmed is a statement about the screen, not a delivery error:
// the pane can be redrawn or scrolled between two snapshots.
type RenderStatus string

const (
	RenderDelivered   RenderStatus = "delivered"
	RenderUnconfirmed RenderStatus = "rendered-unconfirmed"
	RenderFailed      RenderStatus = "failed"
	RenderRetained    RenderStatus = "retained"
)

// RenderResult carries the classification and a short reason for wake reports.
type RenderResult struct {
	Status RenderStatus `json:"status"`
	Reason string       `json:"reason"`
}

const (
	// Only the tail of the capture is compared. Older lines belong to earlier
	// dialogue and must not confirm a fresh render (T3.246).
	renderTailLines = 40
	// When the whole envelope is wrapped or cut, the first 40 characters are
	// still enough to tell a partial render from a different message.
	renderPrefixRunes = 40
)

var (
	ansiEscape     = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)
	paneBorderOnly = regexp.MustCompile(`^[\s─│╭╮╰╯┌┐└┘├┤┬┴┼]*$`)
	paneInputLine  = regexp.MustCompile(`^\s*[>❯›❮](?:\s|$)`)
)

// VerifyRender reads the target pane through the capture port in capture.go and
// reports whether expected is visible on screen. before is a snapshot taken
// before delivery; an empty before means no earlier snapshot was available.
//
// The report distinguishes a confirmed render from an acknowledgement: a
// successful prompt admission says nothing about the pane. A failed or empty
// capture is rendered-unconfirmed with a nil error, so a missing screen never
// becomes a failed delivery. Only caller mistakes (nil runner, empty target or
// expected text) return an error.
func VerifyRender(ctx context.Context, runner CommandRunner, target, before, expected string) (RenderResult, error) {
	if runner == nil {
		return RenderResult{}, ErrNoPaneRunner
	}
	if target == "" {
		return RenderResult{}, ErrNoPaneTarget
	}
	if strings.TrimSpace(expected) == "" {
		return RenderResult{}, errors.New("wake: verify render: expected text is empty")
	}

	after, err := CapturePane(ctx, runner, target)
	if err != nil {
		return RenderResult{Status: RenderUnconfirmed, Reason: "pane capture failed: " + err.Error()}, nil
	}
	return classifyRender(before, after, expected), nil
}

// classifyRender applies the same normalization to both snapshots and to the
// expected text, then compares the tail of the after snapshot. An envelope
// that was already present before delivery is retained, not a fresh render.
func classifyRender(before, after, expected string) RenderResult {
	want := normalizePane(expected)
	if want == "" {
		return RenderResult{Status: RenderUnconfirmed, Reason: "expected text has no visible lines"}
	}

	got := paneTail(normalizePane(after), renderTailLines)
	if got == "" {
		return RenderResult{Status: RenderUnconfirmed, Reason: "pane is empty after normalization"}
	}
	if !strings.Contains(got, want) {
		if hasRenderPrefix(got, want) {
			return RenderResult{Status: RenderUnconfirmed, Reason: "only the first 40 characters of the envelope are visible"}
		}
		return RenderResult{Status: RenderFailed, Reason: "envelope is absent from the pane capture"}
	}
	if strings.Contains(paneTail(normalizePane(before), renderTailLines), want) {
		return RenderResult{Status: RenderRetained, Reason: "envelope was already on screen before delivery"}
	}
	return RenderResult{Status: RenderDelivered, Reason: "envelope is visible in the pane capture"}
}

// normalizePane drops terminal decoration that is not part of the message:
// ANSI control sequences, CRLF, blank lines, box borders and the input prompt.
func normalizePane(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = ansiEscape.ReplaceAllString(s, "")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || paneBorderOnly.MatchString(trimmed) || paneInputLine.MatchString(trimmed) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func paneTail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func hasRenderPrefix(pane, want string) bool {
	prefix := firstRunes(want, renderPrefixRunes)
	return prefix != "" && strings.Contains(pane, prefix)
}

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
