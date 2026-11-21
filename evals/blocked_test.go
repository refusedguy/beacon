//go:build eval

package main

import (
	"slices"
	"testing"
)

func TestBlockDecisionContract(t *testing.T) {
	w := newBlockedWatch("watch:test-block")
	blocked, out := applyBlockDecision(w, "blocked:not_mine", "правки моих нет")
	if out.Action != "blocked" || out.Reason != "правки моих нет" {
		t.Fatalf("block outcome = %s/%s, want blocked/причина", out.Action, out.Reason)
	}
	if blocked.Status != blockStatusBlocked || blocked.Reason != "правки моих нет" {
		t.Errorf("blocked watch = %s/%q, want blocked/причина", blocked.Status, blocked.Reason)
	}
	if daemonWakeAllowed(blocked) {
		t.Error("daemon wake still allowed for a blocked watch")
	}
	if blockedWakeFor(blocked) == nil {
		t.Error("orchestrator wake missing for a blocked watch")
	}
	if w.Status != blockStatusWatching || !daemonWakeAllowed(w) {
		t.Error("applyBlockDecision mutated the original watch")
	}

	again, againOut := applyBlockDecision(blocked, "blocked:not_mine", "правки моих нет")
	if againOut.Action != "noop" || againOut.Reason != "already_blocked" {
		t.Fatalf("repeat block = %s/%s, want noop/already_blocked", againOut.Action, againOut.Reason)
	}
	if !slices.Equal(again.FailedChecks, blocked.FailedChecks) {
		t.Error("repeat block changed failedChecks")
	}

	unknown, unknownOut := applyBlockDecision(w, "blocked:banana", "неизвестный reason")
	if unknownOut.Action != "noop" || unknownOut.Reason != "unknown_reason" ||
		unknown.Status != blockStatusWatching || !daemonWakeAllowed(unknown) {
		t.Errorf("unknown reason changed the watch: %s/%s, status=%s", unknownOut.Action, unknownOut.Reason, unknown.Status)
	}
	alien, alienOut := applyBlockDecision(w, "banana", "")
	if alienOut.Action != "noop" || alienOut.Reason != "unknown_decision" ||
		alien.Status != blockStatusWatching || !daemonWakeAllowed(alien) {
		t.Errorf("unknown decision changed the watch: %s/%s, status=%s", alienOut.Action, alienOut.Reason, alien.Status)
	}
	approved, approveOut := applyBlockDecision(w, "approve", "")
	if approveOut.Action != "noop" || approveOut.Reason != "unknown_decision" ||
		approved.Status != blockStatusWatching {
		t.Errorf("approve changed the watch: %s/%s, status=%s", approveOut.Action, approveOut.Reason, approved.Status)
	}

	resolved, resolvedOut := applyBlockDecision(blocked, decisionBlockedResolved, "")
	if resolvedOut.Action != "unblocked" || resolved.Status != blockStatusWatching ||
		resolved.Reason != "" || !daemonWakeAllowed(resolved) || blockedWakeFor(resolved) != nil {
		t.Errorf("resolve did not restore watching: %s/%s, status=%s, reason=%q",
			resolvedOut.Action, resolvedOut.Reason, resolved.Status, resolved.Reason)
	}
	noop, noopOut := applyBlockDecision(w, decisionBlockedResolved, "")
	if noopOut.Action != "noop" || noopOut.Reason != "not_blocked" {
		t.Errorf("resolve on watching = %s/%s, want noop/not_blocked", noopOut.Action, noopOut.Reason)
	}
}

func TestCIBridgeRefreshContract(t *testing.T) {
	w := newBlockedWatch("watch:test-bridge")
	blocked, _ := applyBlockDecision(w, "blocked:not_mine", "моих правок нет")
	blockedEffects := ciBridgeRefresh(blocked)
	if blockedEffects.Polls != 0 || blockedEffects.Retries != 0 {
		t.Errorf("blocked bridge effects = %+v, want zero", blockedEffects)
	}

	watchingEffects := ciBridgeRefresh(w)
	if watchingEffects.Polls != 1 || watchingEffects.Retries != 1 {
		t.Errorf("watching bridge effects = %+v, want one poll and one retry", watchingEffects)
	}

	clean := w
	clean.FailedChecks = nil
	cleanEffects := ciBridgeRefresh(clean)
	if cleanEffects.Polls != 1 || cleanEffects.Retries != 0 {
		t.Errorf("clean watch bridge effects = %+v, want one poll and no retry", cleanEffects)
	}
}

func TestBlockedWakeContract(t *testing.T) {
	w := newBlockedWatch("watch:test-wake")
	if blockedWakeFor(w) != nil {
		t.Error("watching watch produced a wake")
	}
	blocked, _ := applyBlockDecision(w, "blocked:not_mine", "не моя вина")
	wake := blockedWakeFor(blocked)
	if wake == nil {
		t.Fatal("blocked watch produced no wake")
	}
	if wake.WatchID != blocked.ID || wake.PR != "refusedguy/beacon#299" || wake.Reason != "не моя вина" {
		t.Errorf("wake identity = %q/%q/%q", wake.WatchID, wake.PR, wake.Reason)
	}
	if !slices.Equal(wake.FailedChecks, []string{"ci-audit", "ci-build"}) {
		t.Errorf("wake failedChecks = %v, want sorted checks", wake.FailedChecks)
	}

	wake.FailedChecks[0] = "mutated"
	if !slices.Equal(blocked.FailedChecks, []string{"ci-build", "ci-audit"}) {
		t.Error("wake aliases the watch failedChecks slice")
	}
}

func TestPlainAckKeepsWakeUntilBlock(t *testing.T) {
	w := newBlockedWatch("watch:test-ack")
	plain, out := plainAck(w, "obligation:test")
	if out.Action != "acked" || out.Reason != "claimed" || !plain.ClaimedAt {
		t.Fatalf("plain ack = %s/%s, claimed=%t", out.Action, out.Reason, plain.ClaimedAt)
	}
	if plain.Status != blockStatusWatching || !daemonWakeAllowed(plain) || blockedWakeFor(plain) != nil {
		t.Error("plain ack changed the watch or suppressed the daemon wake")
	}
	if w.ClaimedAt {
		t.Error("plain ack mutated the original watch")
	}

	invalid, invalidOut := plainAck(w, "  ")
	if invalidOut.Action != "noop" || invalidOut.Reason != "invalid_obligation" ||
		invalid.ClaimedAt || !daemonWakeAllowed(invalid) {
		t.Errorf("plain ack with empty obligation = %s/%s, claimed=%t", invalidOut.Action, invalidOut.Reason, invalid.ClaimedAt)
	}

	blocked, _ := applyBlockDecision(plain, "blocked:not_mine", "правки моих нет")
	if daemonWakeAllowed(blocked) || blockedWakeFor(blocked) == nil {
		t.Error("blocked watch must stop the daemon wake and notify the orchestrator")
	}
	resumed, _ := applyBlockDecision(blocked, decisionBlockedResolved, "")
	if !daemonWakeAllowed(resumed) || blockedWakeFor(resumed) != nil {
		t.Error("resolve must restore the daemon wake")
	}
}

func TestBlockedEvalCasesPass(t *testing.T) {
	cases := []struct {
		name string
		run  func() (string, bool)
	}{
		{"B01_block_decision", evalB01BlockDecision},
		{"B02_ci_bridge_skips_blocked", evalB02CIBridgeSkipsBlocked},
		{"B03_blocked_wake", evalB03BlockedWake},
		{"B04_unknown_and_unblock", evalB04UnknownDecisionAndUnblock},
		{"B05_plain_ack_vs_blocked_wake", evalB05PlainAckVsBlockedWake},
	}
	for _, tc := range cases {
		actual, pass := tc.run()
		if !pass {
			t.Errorf("%s failed: %s", tc.name, actual)
		}
	}
}
