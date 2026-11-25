package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var prPattern = regexp.MustCompile(`(?:pull/|"pr"\s*:\s*)(\d+)`)

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

func truncate(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}
