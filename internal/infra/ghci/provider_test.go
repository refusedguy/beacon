package ghci

import "testing"

func TestGhCheckConclusion(t *testing.T) {
	cases := map[string]string{
		"PASS": "SUCCESS", "SUCCESS": "SUCCESS",
		"SKIPPED": "SUCCESS", "NEUTRAL": "SUCCESS",
		"FAIL": "FAILURE", "ERROR": "FAILURE", "TIMED_OUT": "FAILURE",
		"CANCELLED": "CANCELLED",
		"PENDING":   "PENDING", "IN_PROGRESS": "PENDING", "": "PENDING",
	}
	for in, want := range cases {
		if got := ghCheckConclusion(in); got != want {
			t.Errorf("ghCheckConclusion(%q) = %q, want %q", in, got, want)
		}
	}
}
