package shared

import (
	"strings"
	"testing"
)

func TestRedactTelegramToken(t *testing.T) {
	token := "123456789:" + strings.Repeat("A", 35)
	in := "bot " + token + " sent a message"
	out := Redact(in)
	if strings.Contains(out, token) || strings.Contains(out, strings.Repeat("A", 35)) {
		t.Fatalf("telegram token leaked: %s", out)
	}
	if !strings.Contains(out, redacted) {
		t.Fatalf("no mask applied: %s", out)
	}
	if !strings.HasPrefix(out, "bot ") || !strings.HasSuffix(out, " sent a message") {
		t.Fatalf("surrounding text lost: %s", out)
	}
}

func TestRedactURLCredentials(t *testing.T) {
	in := "clone https://alice:s3cr3t@example.com/repo.git"
	out := Redact(in)
	for _, secret := range []string{"s3cr3t", "alice:s3cr3t"} {
		if strings.Contains(out, secret) {
			t.Fatalf("url credentials leaked: %s", out)
		}
	}
	if !strings.Contains(out, redacted) {
		t.Fatalf("no mask applied: %s", out)
	}
	if !strings.Contains(out, "https://") || !strings.Contains(out, "example.com/repo.git") {
		t.Fatalf("url context lost: %s", out)
	}
}

func TestRedactOtherSecrets(t *testing.T) {
	cases := []struct{ in, secret string }{
		{"ghp_abcdefghijklmnopqrstuvwxyz123456", "ghp_abcdefghijklmnopqrstuvwxyz123456"},
		{"github_pat_abcdefghijklmnopqrstuvwxyz123456", "github_pat_abcdefghijklmnopqrstuvwxyz123456"},
		{"xoxb-123456789012-abcdefghijkl", "xoxb-123456789012-abcdefghijkl"},
		{"sk-abcdefghijklmnopqrstuvwxyz", "sk-abcdefghijklmnopqrstuvwxyz"},
		{"AIzaSyAabcdefghijklmnopqrstuvwxyz12345", "AIzaSyAabcdefghijklmnopqrstuvwxyz12345"},
		{"Authorization: Bearer abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"api_key=supersecretvalue; next", "supersecretvalue"},
	}
	for _, tc := range cases {
		out := Redact("prefix " + tc.in + " suffix")
		if strings.Contains(out, tc.secret) {
			t.Errorf("secret leaked: %s", out)
		}
		if !strings.Contains(out, redacted) || !strings.HasPrefix(out, "prefix ") || !strings.HasSuffix(out, " suffix") {
			t.Errorf("bad redaction: %s", out)
		}
	}
}
