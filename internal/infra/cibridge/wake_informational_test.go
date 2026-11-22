package cibridge

import (
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

func informationalWakeObs() ciObservation {
	return ciObservation{pr: domain.PRState{Checks: map[string]domain.Check{
		"ci-build": {Conclusion: "FAILURE", Required: true},
		"ci-info":  {Conclusion: "FAILURE", Required: false},
	}}}
}

// TestWakeTextExcludesInformationalChecks proves the executor is told to fix
// and re-run only the checks that can block a merge (ATX-V03).
func TestWakeTextExcludesInformationalChecks(t *testing.T) {
	obs := informationalWakeObs()
	cases := []struct {
		name    string
		verdict domain.Verdict
		targets []domain.WakeTarget
		want    []string
		absent  []string
	}{
		{
			name:    "keep_working names only the required failure",
			verdict: domain.VerdictKeepWorking,
			want:    []string{"keep_working", "ci-build"},
			absent:  []string{"ci-info"},
		},
		{
			name:    "retry fallback names only the required failure",
			verdict: domain.VerdictRetry,
			want:    []string{"gh run rerun --failed", "ci-build"},
			absent:  []string{"ci-info"},
		},
		{
			name:    "retry keeps explicit rerun checks",
			verdict: domain.VerdictRetry,
			targets: []domain.WakeTarget{{RerunChecks: []string{"ci-build"}}},
			want:    []string{"gh run rerun --failed", "ci-build"},
			absent:  []string{"ci-info"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := wakeText(tc.verdict, obs, tc.targets, "")
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("wake text %q missing %q", text, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(text, absent) {
					t.Fatalf("wake text %q names informational check %q", text, absent)
				}
			}
		})
	}
}

// TestWakeTextInformationalOnlyHasNoActionableCheck proves a failure set with
// only informational checks carries no check name for the executor to fix or
// re-run. The verdict layer never emits keep_working for such a set, so this
// wake can never order a repair of an informational gate.
func TestWakeTextInformationalOnlyHasNoActionableCheck(t *testing.T) {
	obs := ciObservation{pr: domain.PRState{Checks: map[string]domain.Check{
		"ci-info": {Conclusion: "FAILURE", Required: false},
	}}}
	for _, verdict := range []domain.Verdict{domain.VerdictKeepWorking, domain.VerdictRetry} {
		text := wakeText(verdict, obs, nil, "")
		if strings.Contains(text, "ci-info") {
			t.Fatalf("%s wake text %q names informational check", verdict, text)
		}
	}
}
