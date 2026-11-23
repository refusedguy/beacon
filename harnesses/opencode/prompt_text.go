package opencode

import (
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}

func wakeText(w core.WakeContext, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "correction run=%s pr=%d head=%s rebase=%d fix=%d\n", w.RunID, w.PRNumber, w.HeadSHA, w.RebaseTry, w.FixTry)
	fmt.Fprintf(&b, "reason: %s\n", w.Reason)
	if w.Excerpt != "" {
		fmt.Fprintf(&b, "excerpt: %s\n", truncate(w.Excerpt, limit))
	}
	b.WriteString("fix the issue, push, then call beacon_watch_pr again")
	return b.String()
}
