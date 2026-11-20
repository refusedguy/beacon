//go:build eval

package main

import "testing"

func TestStatusOf(t *testing.T) {
	if got := statusOf(true); got != statusPass {
		t.Errorf("statusOf(true) = %q, want %q", got, statusPass)
	}
	if got := statusOf(false); got != statusFail {
		t.Errorf("statusOf(false) = %q, want %q", got, statusFail)
	}
}

func TestStatusBlocks(t *testing.T) {
	cases := []struct {
		status evalStatus
		want   bool
	}{
		{statusPass, false},
		{statusWarn, false},
		{statusFail, true},
		{evalStatus("unknown"), true},
	}
	for _, tc := range cases {
		if got := tc.status.blocks(); got != tc.want {
			t.Errorf("%q.blocks() = %t, want %t", tc.status, got, tc.want)
		}
	}
}

func TestStatusLabel(t *testing.T) {
	cases := map[evalStatus]string{
		statusPass:        "PASS",
		statusWarn:        "WARN",
		statusFail:        "FAIL",
		evalStatus("bad"): "FAIL",
	}
	for status, want := range cases {
		if got := status.label(); got != want {
			t.Errorf("%q.label() = %q, want %q", status, got, want)
		}
	}
}

func TestSummarizeThreeStatuses(t *testing.T) {
	results := []evalResult{
		{name: "a", status: statusPass},
		{name: "b", status: statusPass},
		{name: "c", status: statusWarn},
		{name: "d", status: statusFail},
	}
	got := summarize(results)
	want := evalSummary{passed: 2, warned: 1, failed: 1}
	if got != want {
		t.Errorf("summarize() = %+v, want %+v", got, want)
	}
}

func TestSummaryBlocksOnlyOnFail(t *testing.T) {
	cases := []struct {
		name    string
		summary evalSummary
		want    bool
	}{
		{"pass only", evalSummary{passed: 3}, false},
		{"warn does not block", evalSummary{passed: 2, warned: 1}, false},
		{"fail blocks", evalSummary{passed: 2, warned: 1, failed: 1}, true},
	}
	for _, tc := range cases {
		if got := tc.summary.blocks(); got != tc.want {
			t.Errorf("%s: blocks() = %t, want %t", tc.name, got, tc.want)
		}
	}
}
