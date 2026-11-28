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

func TestWakeTextMatchesPRBinding(t *testing.T) {
	bound := WakeContext{RunID: "run-1", PRNumber: 17, HeadSHA: "abc", Reason: "ci red", Action: WakeFixChecks}
	for name, text := range map[string]string{
		"full":    WakeText(bound),
		"limited": WakeTextLimited(bound, 0),
	} {
		if !strings.Contains(text, ExistingPRInstruction) {
			t.Errorf("%s wake = %q, want the existing-PR instruction", name, text)
		}
		if strings.Contains(text, OpenPRAndWatchInstruction) {
			t.Errorf("%s wake = %q, asked for a second PR", name, text)
		}
		if !strings.Contains(text, "Fix the failed checks") {
			t.Errorf("%s wake = %q, want the criterion-specific action", name, text)
		}
	}
	unbound := WakeContext{RunID: "run-2", Action: WakeFix}
	if !strings.Contains(WakeText(unbound), OpenPRAndWatchInstruction) {
		t.Fatalf("unbound wake does not ask for a PR: %q", WakeText(unbound))
	}
}

func TestGuidanceTextAppendsNoAction(t *testing.T) {
	w := WakeContext{RunID: "run-1", PRNumber: 7, Excerpt: "please rebase first", Action: WakeGuidance}
	for name, text := range map[string]string{
		"direct":  GuidanceText(w),
		"full":    WakeText(w),
		"limited": WakeTextLimited(w, 0),
	} {
		if !strings.Contains(text, "please rebase first") {
			t.Errorf("%s guidance = %q, lost the message", name, text)
		}
		for _, unwanted := range []string{"Fix the issue", "push and", "pull request"} {
			if strings.Contains(text, unwanted) {
				t.Errorf("%s guidance = %q, appended %q", name, text, unwanted)
			}
		}
	}
}

func TestIdleTextOmitsZeroPRAndPlaceholderHead(t *testing.T) {
	w := WakeContext{RunID: "run-1", HeadSHA: IdleHead, Branch: "beacon/T-1", Reason: "idle without PR", Action: WakeIdle}
	for name, text := range map[string]string{
		"direct":  IdleText(w),
		"full":    WakeText(w),
		"limited": WakeTextLimited(w, 0),
	} {
		if strings.Contains(text, "PR #0") || strings.Contains(text, "head "+IdleHead) {
			t.Errorf("%s idle text = %q, printed a placeholder", name, text)
		}
		if n := strings.Count(text, OpenPRAndWatchInstruction); n != 1 {
			t.Errorf("%s idle text = %q, instruction appears %d times", name, text, n)
		}
	}
	bound := w
	bound.PRNumber = 9
	bound.HeadSHA = "real-sha"
	text := IdleText(bound)
	if !strings.Contains(text, "PR #9") || !strings.Contains(text, "head real-sha") {
		t.Fatalf("idle text = %q, want the real PR and SHA", text)
	}
}

func TestWakeTextLimitedTruncatesReason(t *testing.T) {
	long := strings.Repeat("r", 200)
	w := WakeContext{Reason: long, Excerpt: "short", Action: WakeFix}
	got := WakeTextLimited(w, 20)
	if strings.Contains(got, long) {
		t.Fatalf("limited wake kept the full reason: %q", got)
	}
	if !strings.Contains(got, "reason: "+strings.Repeat("r", 20)+excerptMarker) {
		t.Fatalf("limited wake = %q, want a truncated reason", got)
	}
}

func TestWakeActionForDecisionNamesTheCriterion(t *testing.T) {
	cases := []struct {
		name string
		dec  MergeDecision
		want WakeAction
	}{
		{"conflict", MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: "merge conflict", Criterion: "invariant"}, WakeFixConflict},
		{"threads", MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: "unresolved discussion: thread", Criterion: "invariant"}, WakeFixThreads},
		{"check", MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: "required check failed: build", Criterion: "invariant"}, WakeFixChecks},
		{"rule", MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: "policy", Criterion: "approvals"}, WakeFixChecks},
		{"generic", MergeDecision{Verdict: VerdictNeedsAgentFix, Reason: "test", Criterion: "invariant"}, WakeFix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WakeActionFor(tc.dec); got != tc.want {
				t.Fatalf("WakeActionFor(%+v) = %q, want %q", tc.dec, got, tc.want)
			}
		})
	}
}
