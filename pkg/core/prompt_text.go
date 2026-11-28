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

// OpenPRAndWatchInstruction is the single statement telling an agent to open
// a pull request and bind the run with beacon_watch_pr. The spawn prompt, the
// idle nudge and fixer prompts for an unbound run use it, so the instruction
// has one wording. It starts lowercase so a caller can place it after its own
// lead-in.
const OpenPRAndWatchInstruction = "open a pull request, then call beacon_watch_pr with the run ID and PR number."

// ExistingPRInstruction tells a woken agent to push the fix to the pull request
// already bound to its run. A wake with a bound PR must not ask for a second PR.
// It starts lowercase for the same reason as OpenPRAndWatchInstruction.
const ExistingPRInstruction = "push the fix to the existing pull request."

// IdleHead stands in for the head SHA of a run that has no PR. The idle prompt
// omits it instead of printing a fake commit.
const IdleHead = "idle"

// WakeAction is the one action a woken agent carries out. Renderers use it to
// pick a prompt that matches the agent's situation instead of appending the
// same fix-and-PR command to every wake.
type WakeAction string

const (
	// WakeFix is the generic fix for a reason that names no specific criterion.
	WakeFix WakeAction = "fix"
	// WakeFixChecks asks the agent to repair failed checks.
	WakeFixChecks WakeAction = "fix_checks"
	// WakeFixConflict asks the agent to resolve a merge conflict.
	WakeFixConflict WakeAction = "fix_conflict"
	// WakeFixThreads asks the agent to answer unresolved review threads.
	WakeFixThreads WakeAction = "fix_threads"
	// WakeIdle asks the agent to push its changes and open a PR.
	WakeIdle WakeAction = "idle"
	// WakeGuidance delivers operator text with no appended action.
	WakeGuidance WakeAction = "guidance"
)

// WakeActionFor derives the worker action from a merge decision. The verdict
// names the kind of problem and the criterion names the check that stopped the
// merge, so the prompt can name what the worker must fix.
func WakeActionFor(d MergeDecision) WakeAction {
	if d.Verdict != VerdictNeedsAgentFix && d.Verdict != VerdictNeedsRebase {
		return WakeFix
	}
	switch {
	case strings.HasPrefix(d.Reason, "merge conflict"):
		return WakeFixConflict
	case strings.HasPrefix(d.Reason, "unresolved discussion"):
		return WakeFixThreads
	case strings.HasPrefix(d.Reason, "required check failed"), d.Criterion != "" && d.Criterion != "invariant":
		return WakeFixChecks
	default:
		return WakeFix
	}
}

// FixInstruction renders the action phrase carried by a wake. An empty or
// unknown action keeps the generic wording.
func FixInstruction(a WakeAction) string {
	switch a {
	case WakeFixChecks:
		return "Fix the failed checks"
	case WakeFixConflict:
		return "Resolve the merge conflict"
	case WakeFixThreads:
		return "Address the unresolved review threads"
	default:
		return "Fix the issue"
	}
}

// wakeInstruction picks the instruction that matches the run's PR binding.
func wakeInstruction(pr int) string {
	if pr > 0 {
		return ExistingPRInstruction
	}
	return OpenPRAndWatchInstruction
}

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
// The action chooses the template, so an operator nudge and an idle turn never
// receive a fix command they did not ask for.
func WakeText(w WakeContext) string {
	switch w.Action {
	case WakeGuidance:
		return GuidanceText(w)
	case WakeIdle:
		return IdleText(w)
	}
	return fmt.Sprintf("Beacon correction for run %s, PR #%d, head %s.\nReason: %s\n%s\n%s, push and %s\n",
		w.RunID, w.PRNumber, w.HeadSHA, w.Reason, w.Excerpt, FixInstruction(w.Action), wakeInstruction(w.PRNumber))
}

// WakeTextLimited renders a correction prompt that carries retry counters and
// caps the excerpt. A non-positive limit keeps the full text. The reason is
// capped too: a failed check already puts the whole excerpt into the reason,
// so leaving it long would defeat the limit.
func WakeTextLimited(w WakeContext, limit int) string {
	switch w.Action {
	case WakeGuidance:
		return GuidanceText(w)
	case WakeIdle:
		return IdleText(w)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "correction run=%s pr=%d head=%s rebase=%d fix=%d\n",
		w.RunID, w.PRNumber, w.HeadSHA, w.RebaseTry, w.FixTry)
	fmt.Fprintf(&b, "reason: %s\n", TruncateExcerpt(w.Reason, limit))
	if w.Excerpt != "" {
		fmt.Fprintf(&b, "excerpt: %s\n", TruncateExcerpt(w.Excerpt, limit))
	}
	fmt.Fprintf(&b, "%s, push, %s", FixInstruction(w.Action), wakeInstruction(w.PRNumber))
	return b.String()
}

// GuidanceText renders an operator nudge. It carries the message once and
// appends no fix, push or PR instruction, so the worker reads guidance rather
// than a rewritten fix command.
func GuidanceText(w WakeContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Beacon guidance for run %s.\n", w.RunID)
	if w.Excerpt != "" {
		fmt.Fprintf(&b, "%s\n", w.Excerpt)
	}
	return b.String()
}

// IdleText renders the no-PR nudge. It prints a PR and a head only when they
// are real, so an idle run cannot show PR #0 or a placeholder SHA. The shared
// PR-and-watch instruction appears exactly once.
func IdleText(w WakeContext) string {
	var b strings.Builder
	b.WriteString("Beacon nudge for run ")
	b.WriteString(string(w.RunID))
	if w.PRNumber > 0 {
		fmt.Fprintf(&b, ", PR #%d", w.PRNumber)
	}
	if w.HeadSHA != "" && w.HeadSHA != IdleHead {
		b.WriteString(", head ")
		b.WriteString(w.HeadSHA)
	}
	b.WriteString(".\n")
	if w.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", w.Reason)
	}
	fmt.Fprintf(&b, "You finished a turn on branch %s. Push your changes, %s\n", w.Branch, wakeInstruction(w.PRNumber))
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
