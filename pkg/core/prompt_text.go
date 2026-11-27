package core

import (
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

// OpenPRAndWatchInstruction is the single statement telling an agent to open
// a pull request and bind the run with beacon_watch_pr. The spawn prompt, both
// correction prompts and the idle nudge all use it, so the instruction has one
// wording. It starts lowercase so a caller can place it after its own lead-in.
const OpenPRAndWatchInstruction = "open a pull request, then call beacon_watch_pr with the run ID and PR number."

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

// writeInt appends a decimal integer without boxing it through fmt. The
// scratch array holds the digits on the stack until Builder.Write copies them.
func writeInt(b *strings.Builder, n int) {
	var digits [20]byte
	b.Write(strconv.AppendInt(digits[:0], int64(n), 10))
}

// WakeText renders the correction prompt used by the ACP and CLI harnesses.
func WakeText(w WakeContext) string {
	var b strings.Builder
	b.Grow(len("Beacon correction for run ") + len(w.RunID) + len(", PR #") + 20 +
		len(", head ") + len(w.HeadSHA) + len(".\nReason: ") + len(w.Reason) +
		len("\n") + len(w.Excerpt) + len("\nFix the issue, push and ") +
		len(OpenPRAndWatchInstruction) + len("\n"))
	b.WriteString("Beacon correction for run ")
	b.WriteString(string(w.RunID))
	b.WriteString(", PR #")
	writeInt(&b, w.PRNumber)
	b.WriteString(", head ")
	b.WriteString(w.HeadSHA)
	b.WriteString(".\nReason: ")
	b.WriteString(w.Reason)
	b.WriteByte('\n')
	b.WriteString(w.Excerpt)
	b.WriteString("\nFix the issue, push and ")
	b.WriteString(OpenPRAndWatchInstruction)
	b.WriteByte('\n')
	return b.String()
}

// WakeTextLimited renders a correction prompt that carries retry counters and
// caps the excerpt. A non-positive limit keeps the full excerpt.
func WakeTextLimited(w WakeContext, limit int) string {
	excerpt := ""
	if w.Excerpt != "" {
		excerpt = TruncateExcerpt(w.Excerpt, limit)
	}
	var b strings.Builder
	b.Grow(len("correction run=") + len(w.RunID) + len(" pr=") + 20 +
		len(" head=") + len(w.HeadSHA) + len(" rebase=") + 20 + len(" fix=") + 20 +
		len("\nreason: ") + len(w.Reason) + len("\nexcerpt: ") + len(excerpt) +
		len("\n") + len("fix the issue, push, ") +
		len(OpenPRAndWatchInstruction))
	b.WriteString("correction run=")
	b.WriteString(string(w.RunID))
	b.WriteString(" pr=")
	writeInt(&b, w.PRNumber)
	b.WriteString(" head=")
	b.WriteString(w.HeadSHA)
	b.WriteString(" rebase=")
	writeInt(&b, w.RebaseTry)
	b.WriteString(" fix=")
	writeInt(&b, w.FixTry)
	b.WriteString("\nreason: ")
	b.WriteString(w.Reason)
	b.WriteByte('\n')
	if w.Excerpt != "" {
		b.WriteString("excerpt: ")
		b.WriteString(excerpt)
		b.WriteByte('\n')
	}
	b.WriteString("fix the issue, push, " + OpenPRAndWatchInstruction)
	return b.String()
}

// excerptMarker is the single suffix appended when an excerpt is cut short.
// Wake text and fixer prompts share it so fixtures parse one marker.
const excerptMarker = "..."

// TruncateExcerpt caps s at limit runes and appends excerptMarker when text is
// removed. A byte length check keeps short strings allocation free, and valid
// UTF-8 is walked only up to the cap. Invalid UTF-8 keeps the eager conversion
// so replacement bytes stay identical.
func TruncateExcerpt(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	if !utf8.ValidString(s) {
		r := []rune(s)
		if len(r) <= limit {
			return s
		}
		return string(r[:limit]) + excerptMarker
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
	return s[:i] + excerptMarker
}
