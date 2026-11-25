// Package redact removes secrets from text. It is the single source of
// redaction rules for both Go modules.
package redact

import "regexp"

// Marker replaces every matched secret.
const Marker = "[REDACTED]"

type pattern struct {
	re   *regexp.Regexp
	repl string
}

var patterns = []pattern{
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), Marker},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), Marker},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), Marker},
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`), Marker},
	{regexp.MustCompile(`AIza[A-Za-z0-9_-]{30,}`), Marker},
	{regexp.MustCompile(`[0-9]+:[A-Za-z0-9_-]{35}`), Marker},
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/@\s:]+):([^/@\s]+@)`), "${1}:" + Marker + "@"},
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"']+`), "${1}" + Marker},
	{regexp.MustCompile(`(?i)((?:token|secret|password|api[_-]?key)\s*[:=]\s*)"[^"]+"`), "${1}" + Marker},
	{regexp.MustCompile(`(?i)((?:token|secret|password|api[_-]?key)\s*[:=]\s*)[^\s,;}]+`), "${1}" + Marker},
}

// Redact replaces secrets in s with Marker. It preserves the surrounding text.
func Redact(s string) string {
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
