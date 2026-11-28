package redact

import (
	"regexp"
	"strings"
	"testing"
)

// legacyPatterns is the ten-pass chain that Redact used before the prefilter
// and the alternation regex. Tests compare it with Redact so a rule cannot be
// lost or widened by the rewrite.
var legacyPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
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

func legacyRedact(s string) string {
	for _, p := range legacyPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

var redactCases = []struct {
	name      string
	in        string
	redacted  bool
	candidate bool
}{
	{"github token", "token ghp_abcdefghijklmnopqrstuvwxyz123456 in log line", true, true},
	{"github pat", "PAT github_pat_abcdefghijklmnopqrstuvwxyz123456", true, true},
	{"slack token", "slack xoxb-123456789012-abcdefghijkl", true, true},
	{"openai key", "openai sk-abcdefghijklmnopqrstuvwxyz", true, true},
	{"google key", "google AIzaSyAabcdefghijklmnopqrstuvwxyz12345", true, true},
	{"telegram token", "telegram 123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", true, true},
	{"url credentials", "clone https://ci:s3cr3t@example.com/beacon.git", true, true},
	{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz", true, true},
	{"quoted token", `config token="supersecret value with spaces" verbosity=2`, true, true},
	{"api key equals", "api_key=supersecretvalue; next", true, true},
	{"api key dash colon", "api-key: s3cr3t value", true, true},
	{"api key no separator", "apikey=s3cr3t value", true, true},
	{"uppercase secret", "SECRET: hunter2, other=1", true, true},
	{"password spaces", "password = hunter2 next", true, true},
	{"token before secret", "token=abc123 secret=def456", true, true},
	{"secret before bearer", "secret=abc123 Authorization: Bearer def456", true, true},
	{"adjacent labels", "token=abcsecret=\"xyz\"", true, true},
	{"bare and labelled", "ghp_abcdefghijklmnopqrstuvwxyz123456 token=abc123", true, true},
	{"two bare tokens", "ghp_abcdefghijklmnopqrstuvwxyz123456 and sk-abcdefghijklmnopqrstuvwxyz", true, true},
	{"url and token", "token=https://ci:s3cr3t@example.com/beacon.git", true, true},
	{"quoted then unquoted", `token="abc def" secret=ghi`, true, true},
	{"quote inside value", `token=abc"def`, true, true},
	{"space before quote", `token= "abc def"`, true, true},
	{"no secret", "plain text with no secret and a long tail of ordinary words", false, false},
	{"colon no secret", "key: value with spaces and no secret", false, true},
	{"equals no secret", "count=42 and mode=fast", false, true},
	{"short github token", "ghp_short", false, true},
	{"short google key", "AIzaShort", false, true},
	{"quoted bearer", `Authorization: Bearer "quoted value"`, false, true},
	{"empty", "", false, false},
}

func TestRedactMatchesLegacy(t *testing.T) {
	for _, tc := range redactCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			want := legacyRedact(tc.in)
			if got != want {
				t.Fatalf("Redact(%q) = %q, legacy = %q", tc.in, got, want)
			}
			if tc.redacted && !strings.Contains(got, Marker) {
				t.Errorf("expected a marker in %q", got)
			}
			if !tc.redacted && got != tc.in {
				t.Errorf("expected unchanged text, got %q", got)
			}
		})
	}
}

// TestRedactOpenCodeToken covers the opencode:HEX credential shape used by
// the harness. It is separate from redactCases because the legacy chain in
// that table predates this rule.
func TestRedactOpenCodeToken(t *testing.T) {
	const token = "opencode:deadbeef0123456789"
	in := "harness auth failed for " + token + " on session"
	got := Redact(in)
	if strings.Contains(got, token) {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, Marker) {
		t.Fatalf("no marker in %q", got)
	}
	if !strings.HasPrefix(got, "harness auth failed for ") || !strings.HasSuffix(got, " on session") {
		t.Fatalf("surrounding text lost: %q", got)
	}
	if !HasCandidate(token) {
		t.Fatal("HasCandidate(opencode token) = false")
	}
}

func TestHasCandidate(t *testing.T) {
	for _, tc := range redactCases {
		if tc.redacted && !HasCandidate(tc.in) {
			t.Errorf("HasCandidate(%q) = false for redacted text", tc.in)
		}
		if got := HasCandidate(tc.in); got != tc.candidate {
			t.Errorf("HasCandidate(%q) = %t, want %t", tc.in, got, tc.candidate)
		}
	}
}

func TestRedactSurroundingText(t *testing.T) {
	in := "prefix ghp_abcdefghijklmnopqrstuvwxyz123456 suffix"
	out := Redact(in)
	if !strings.HasPrefix(out, "prefix ") || !strings.HasSuffix(out, " suffix") {
		t.Fatalf("surrounding text lost: %q", out)
	}
	if strings.Contains(out, "ghp_abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("secret leaked: %q", out)
	}
}

var redactSink string

func BenchmarkRedactAlternating(b *testing.B) {
	texts := make([]string, len(redactCases))
	for i, tc := range redactCases {
		texts[i] = tc.in
		if got, want := Redact(tc.in), legacyRedact(tc.in); got != want {
			b.Fatalf("Redact(%q) = %q, legacy = %q", tc.in, got, want)
		}
	}
	b.Run("legacy", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, s := range texts {
				redactSink = legacyRedact(s)
			}
		}
	})
	b.Run("prefilter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, s := range texts {
				redactSink = Redact(s)
			}
		}
	})
}
