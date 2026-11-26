package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var prPattern = regexp.MustCompile(`(?:pull/|"pr"\s*:\s*)(\d+)`)

// WeakMachineVerificationRule is the single statement of the verification rule
// shared by config defaults, init stack variants and the spawn prompt. Heavy
// verification is advisory on a weak machine and runs in CI; the local format
// and targeted tests stay required. A full machine runs the full suite before
// reporting done. Callers append their own language-specific check command
// instead of restating the rule, so one prompt cannot carry opposing advice.
const WeakMachineVerificationRule = "On a weak machine, the full test suite, the full build and repository linters are advisory. Leave them to CI. A weak PC runs the local format and targeted tests instead; those checks are required on every machine. On a full machine, run the full test suite, the full build and the repository linters before reporting done."

// PRFromText returns the pull request number an agent printed. Both the URL
// shape (pull/17) and the JSON shape ("pr": 17) are accepted. It returns zero
// when the text contains no number.
func PRFromText(s string) int {
	m := prPattern.FindStringSubmatch(s)
	if len(m) != 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// WakeText renders the correction prompt used by the ACP and CLI harnesses.
func WakeText(w WakeContext) string {
	return fmt.Sprintf("Beacon correction for run %s, PR #%d, head %s.\nReason: %s\n%s\nFix the issue, push and open a PR.\n",
		w.RunID, w.PRNumber, w.HeadSHA, w.Reason, w.Excerpt)
}

// WakeTextLimited renders a correction prompt that carries retry counters and
// caps the excerpt. A non-positive limit keeps the full excerpt.
func WakeTextLimited(w WakeContext, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "correction run=%s pr=%d head=%s rebase=%d fix=%d\n",
		w.RunID, w.PRNumber, w.HeadSHA, w.RebaseTry, w.FixTry)
	fmt.Fprintf(&b, "reason: %s\n", w.Reason)
	if w.Excerpt != "" {
		fmt.Fprintf(&b, "excerpt: %s\n", truncate(w.Excerpt, limit))
	}
	b.WriteString("fix the issue, push, then call beacon_watch_pr again")
	return b.String()
}

// truncate caps s at limit runes. A byte length check keeps short strings
// allocation free, and valid UTF-8 is walked only up to the cap. Invalid UTF-8
// keeps the eager conversion so replacement bytes stay identical.
func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	if !utf8.ValidString(s) {
		r := []rune(s)
		if len(r) <= limit {
			return s
		}
		return string(r[:limit]) + "..."
	}
	i, n := 0, 0
	for i < len(s) && n < limit {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	if i == len(s) {
		return s
	}
	return s[:i] + "..."
}
