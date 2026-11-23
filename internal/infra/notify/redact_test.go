package notify

import "testing"

func TestRedact(t *testing.T) {
	in := "token ghp_abcdefghijklmnopqrstuvwxyz123456 and Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz"
	out := Redact(in)
	for _, secret := range []string{"ghp_abcdefghijklmnopqrstuvwxyz123456", "sk-abcdefghijklmnopqrstuvwxyz"} {
		if contains(out, secret) {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	if !contains(out, "[REDACTED]") {
		t.Fatalf("nothing redacted: %s", out)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && index(s, sub) >= 0
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
