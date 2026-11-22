package orchestrator

import "testing"

// TestAllowAutofixSkipReasons checks that each skip condition returns its own
// reason. The cases are independent so a swapped rule cannot pass by returning
// any non-allowed value.
func TestAllowAutofixSkipReasons(t *testing.T) {
	tests := []struct {
		name  string
		facts AutofixFacts
		want  AutofixSkipReason
	}{
		{
			name: "human pushed the head commit",
			facts: AutofixFacts{
				HeadPusherIsAgent: false,
			},
			want: AutofixSkipHumanHead,
		},
		{
			name: "follow-up after the last attempt",
			facts: AutofixFacts{
				HeadPusherIsAgent:        true,
				FollowUpSinceLastAttempt: true,
			},
			want: AutofixSkipFollowUp,
		},
		{
			name: "check already fails on the base branch",
			facts: AutofixFacts{
				HeadPusherIsAgent: true,
				BaseFailedChecks:  []string{"build"},
			},
			want: AutofixSkipBaseFailure,
		},
		{
			name: "tenth attempt",
			facts: AutofixFacts{
				HeadPusherIsAgent: true,
				AttemptCount:      DefaultAutofixAttemptLimit,
			},
			want: AutofixSkipAttemptLimit,
		},
		{
			name: "owner opted out",
			facts: AutofixFacts{
				HeadPusherIsAgent: true,
				OptedOut:          true,
			},
			want: AutofixSkipOptedOut,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AllowAutofix(tt.facts, 0); got != tt.want {
				t.Fatalf("AllowAutofix() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestAllowAutofixGreenAgentHeadPasses guards the happy path. Green CI with an
// agent-authored head spends no skip reason, so the fixer spawn stays allowed.
func TestAllowAutofixGreenAgentHeadPasses(t *testing.T) {
	facts := AutofixFacts{
		HeadPusherIsAgent:        true,
		FollowUpSinceLastAttempt: false,
		BaseFailedChecks:         nil,
		AttemptCount:             DefaultAutofixAttemptLimit - 1,
		OptedOut:                 false,
	}
	if got := AllowAutofix(facts, 0); got != AutofixAllowed {
		t.Fatalf("AllowAutofix() = %s, want allowed", got)
	}
}

// TestAllowAutofixCustomLimit checks that the caller can move the attempt
// budget and that a zero limit falls back to the default.
func TestAllowAutofixCustomLimit(t *testing.T) {
	facts := AutofixFacts{
		HeadPusherIsAgent: true,
		AttemptCount:      3,
	}
	if got := AllowAutofix(facts, 2); got != AutofixSkipAttemptLimit {
		t.Fatalf("AllowAutofix(limit=2) = %s, want attempt limit reached", got)
	}
	if got := AllowAutofix(facts, 4); got != AutofixAllowed {
		t.Fatalf("AllowAutofix(limit=4) = %s, want allowed", got)
	}
	if got := AllowAutofix(facts, 0); got != AutofixAllowed {
		t.Fatalf("AllowAutofix(limit=0) = %s, want default limit and allowed", got)
	}
}
