package domain

import "testing"

func TestAttentionForVerdictClassifiesBlockingVerdicts(t *testing.T) {
	tests := []struct {
		verdict string
		want    Attention
	}{
		{"retry", Attention{Level: AttentionAction, Reason: AttentionRetry, NextAction: "rerun the failed checks"}},
		{"rebase", Attention{Level: AttentionAction, Reason: AttentionRebase, NextAction: "rebase onto origin/main"}},
		{"keep_working", Attention{Level: AttentionAction, Reason: AttentionKeepWorking, NextAction: "resume the failed checks"}},
		{"timeout", Attention{Level: AttentionAction, Reason: AttentionTimeout, NextAction: "inspect the Watch and decide whether to continue"}},
		{" REBASE ", Attention{Level: AttentionAction, Reason: AttentionRebase, NextAction: "rebase onto origin/main"}},
		{"Keep_Working", Attention{Level: AttentionAction, Reason: AttentionKeepWorking, NextAction: "resume the failed checks"}},
	}
	for _, tt := range tests {
		t.Run(tt.verdict, func(t *testing.T) {
			got := AttentionForVerdict(tt.verdict)
			if got != tt.want {
				t.Errorf("AttentionForVerdict(%q) = %+v, want %+v", tt.verdict, got, tt.want)
			}
			if !got.NeedsAction() {
				t.Errorf("AttentionForVerdict(%q).NeedsAction() = false, want true", tt.verdict)
			}
		})
	}
}

func TestAttentionForVerdictLeavesUnrecognisedSourcesUnclassified(t *testing.T) {
	for _, verdict := range []string{"", "ready", "watching", "login_required", "trust_gate"} {
		got := AttentionForVerdict(verdict)
		if got != (Attention{}) {
			t.Errorf("AttentionForVerdict(%q) = %+v, want zero value", verdict, got)
		}
		if got.NeedsAction() {
			t.Errorf("AttentionForVerdict(%q).NeedsAction() = true, want false", verdict)
		}
		if compact := got.Compact(); compact != "?" {
			t.Errorf("AttentionForVerdict(%q).Compact() = %q, want ?", verdict, compact)
		}
	}
}

func TestAttentionCompactCodes(t *testing.T) {
	tests := []struct {
		reason AttentionReason
		want   string
	}{
		{AttentionRetry, "r"},
		{AttentionRebase, "b"},
		{AttentionKeepWorking, "k"},
		{AttentionTimeout, "t"},
		{AttentionReason("unknown"), "?"},
	}
	for _, tt := range tests {
		a := Attention{Level: AttentionAction, Reason: tt.reason}
		if got := a.Compact(); got != tt.want {
			t.Errorf("Compact() for reason %q = %q, want %q", tt.reason, got, tt.want)
		}
	}
}
