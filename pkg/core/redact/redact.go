// Package redact removes secrets from text. It is the single source of
// redaction rules for both Go modules.
package redact

import (
	"regexp"
	"strings"
)

// Marker replaces every matched secret.
const Marker = "[REDACTED]"

type rule struct {
	re     *regexp.Regexp
	prefix string
	repl   string
}

func newRule(expr, repl string) rule {
	re := regexp.MustCompile(expr)
	prefix, _ := re.LiteralPrefix()
	return rule{re: re, prefix: prefix, repl: repl}
}

// bareRules match token-shaped secrets that are replaced in full. Their order
// is the order of the original single-purpose patterns.
var bareRules = []rule{
	newRule(`gh[pousr]_[A-Za-z0-9]{20,}`, Marker),
	newRule(`github_pat_[A-Za-z0-9_]{20,}`, Marker),
	newRule(`xox[baprs]-[A-Za-z0-9-]{10,}`, Marker),
	newRule(`sk-[A-Za-z0-9_-]{20,}`, Marker),
	newRule(`AIza[A-Za-z0-9_-]{30,}`, Marker),
	newRule(`[0-9]+:[A-Za-z0-9_-]{35}`, Marker),
}

// urlRule drops the user and password from a URL but keeps the scheme and host.
var urlRule = newRule(`([A-Za-z][A-Za-z0-9+.-]*://[^/@\s:]+):([^/@\s]+@)`, "${1}:"+Marker+"@")

// labelledRule keeps the label before a secret and replaces the value. The
// three label rules share a replacement, so one alternation does the work.
// Exactly one of the three groups captures the label.
var labelledRule = newRule(
	`(?i)(authorization:\s*bearer\s+)[^\s"']+`+
		`|((?:token|secret|password|api[_-]?key)\s*[:=]\s*)"[^"]+"`+
		`|((?:token|secret|password|api[_-]?key)\s*[:=]\s*)[^\s,;}]+`,
	`${1}${2}${3}`+Marker,
)

// HasCandidate reports whether s contains a byte that a redaction rule needs.
// A false result means Redact returns s unchanged.
func HasCandidate(s string) bool {
	return strings.ContainsAny(s, ":_=") ||
		strings.Contains(s, "AIza") ||
		strings.Contains(s, "xox") ||
		strings.Contains(s, "sk-")
}

// Redact replaces secrets in s with Marker. It preserves the surrounding text.
func Redact(s string) string {
	if !HasCandidate(s) {
		return s
	}
	for _, r := range bareRules {
		if r.prefix != "" && !strings.Contains(s, r.prefix) {
			continue
		}
		s = r.re.ReplaceAllString(s, r.repl)
	}
	if strings.Contains(s, "://") {
		s = urlRule.re.ReplaceAllString(s, urlRule.repl)
	}
	return labelledRule.re.ReplaceAllString(s, labelledRule.repl)
}
