package orchestrator

import (
	"testing"

	"github.com/refusedguy/beacon/internal/domain"
)

// TestVerdictMirrorsMatchDomain keeps the app-layer verdict mirrors in sync
// with the domain contract. ADR 0005 forbids app -> domain imports in
// production code, so the mirrors are compared here instead of derived at
// compile time. A value that lives only in a mirror fails this test.
func TestVerdictMirrorsMatchDomain(t *testing.T) {
	domainSet := make(map[string]bool)
	for _, v := range domain.Verdicts() {
		domainSet[string(v)] = true
	}

	// The contract owns every value that used to live only in a mirror or in a
	// helper file.
	for _, v := range []domain.Verdict{
		domain.VerdictFail, domain.VerdictWait, domain.VerdictWaitDependency,
		domain.VerdictGreenBlocked, domain.VerdictBlocked, domain.VerdictRecheck,
		domain.VerdictNeedsInput, domain.VerdictParked, domain.VerdictAccepted,
	} {
		if !domainSet[string(v)] {
			t.Errorf("domain contract misses %q", v)
		}
	}
	if got, want := string(domain.GreenBlocked), string(domain.VerdictGreenBlocked); got != want {
		t.Errorf("green blocked values differ: verdict.go %q, contract %q", got, want)
	}

	// Every string the app can emit as a verdict must be in the contract.
	mirrors := map[string]string{
		"checkall.watching":    string(CheckVerdictWatching),
		"checkall.ready":       string(CheckVerdictReady),
		"checkall.retry":       string(CheckVerdictRetry),
		"orchestrator.ready":   VerdictReady,
		"orchestrator.wait":    VerdictWait,
		"orchestrator.hold":    VerdictWaitDependency,
		"orchestrator.retry":   VerdictRetry,
		"orchestrator.fail":    VerdictFail,
		"rebase.rebase":        VerdictRebase,
		"workload.watching":    workloadVerdictWatching,
		"workload.rebase":      workloadVerdictRebase,
		"workload.keepWorking": workloadVerdictKeepWorking,
		"workload.timeout":     workloadVerdictTimeout,
	}
	mirrorSet := make(map[string]bool, len(mirrors))
	for name, value := range mirrors {
		mirrorSet[value] = true
		if !domainSet[value] {
			t.Errorf("%s = %q, not in the domain verdict contract", name, value)
		}
	}

	// Each verdict this package emits has a mirror, so a rename in the contract
	// cannot leave the app on an old string. Blocked is produced outside this
	// package and its mirror is mcp.NextActionBlocked, which derives from
	// domain.VerdictBlocked in schema.go.
	for _, v := range []domain.Verdict{
		domain.VerdictWatching, domain.VerdictReady, domain.VerdictRebase,
		domain.VerdictRetry, domain.VerdictKeepWorking, domain.VerdictTimeout,
		domain.VerdictFail, domain.VerdictWait, domain.VerdictWaitDependency,
	} {
		if !mirrorSet[string(v)] {
			t.Errorf("domain verdict %q has no app mirror", v)
		}
	}

	// The set helpers published by the mirrors agree with the contract too.
	for _, v := range CheckVerdicts() {
		if !domainSet[string(v)] {
			t.Errorf("checkall set contains %q, not in the domain verdict contract", v)
		}
	}
	for _, v := range OrchestratorVerdicts() {
		if !domainSet[v] {
			t.Errorf("orchestrator set contains %q, not in the domain verdict contract", v)
		}
	}
}
