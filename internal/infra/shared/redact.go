// Package shared holds the only secret redaction implementation and common helpers.
package shared

import "regexp"

const redacted = "[REDACTED]"

type secretPattern struct {
	re   *regexp.Regexp
	repl string
}

var secretPatterns = []secretPattern{
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), redacted},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), redacted},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), redacted},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`), redacted},
	{regexp.MustCompile(`AIza[A-Za-z0-9_-]{30,}`), redacted},
	{regexp.MustCompile(`[0-9]+:[A-Za-z0-9_-]{35}`), redacted},
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/@\s:]+):([^/@\s]+@)`), "${1}:" + redacted + "@"},
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"']+`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)((?:token|secret|password|api[_-]?key)\s*[:=]\s*)"[^"]+"`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)((?:token|secret|password|api[_-]?key)\s*[:=]\s*)[^\s,;}]+`), "${1}" + redacted},
}

func Redact(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
