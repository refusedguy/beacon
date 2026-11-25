package core

import (
	"strings"
	"testing"
)

func TestPRFromTextShapes(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"url", "opened pull/17 for review", 17},
		{"json", `{"pr": 42, "state": "open"}`, 42},
		{"json spaced", `"pr" : 7`, 7},
		{"none", "no pr here", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PRFromText(tc.text); got != tc.want {
				t.Fatalf("PRFromText(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestWakeTextCarriesContext(t *testing.T) {
	w := WakeContext{
		RunID:     "run-1",
		PRNumber:  17,
		HeadSHA:   "abc123",
		Reason:    "review blocked",
		Excerpt:   "gate failed",
		RebaseTry: 2,
		FixTry:    3,
	}
	got := WakeText(w)
	for _, want := range []string{"run-1", "PR #17", "abc123", "review blocked", "gate failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("WakeText = %q, want it to contain %q", got, want)
		}
	}
	limited := WakeTextLimited(w, 4)
	for _, want := range []string{"pr=17", "rebase=2", "fix=3", "gate..."} {
		if !strings.Contains(limited, want) {
			t.Fatalf("WakeTextLimited = %q, want it to contain %q", limited, want)
		}
	}
	if strings.Contains(limited, "gate failed") {
		t.Fatalf("WakeTextLimited = %q, want a truncated excerpt", limited)
	}
}
