package main

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/internal/app/orchestrator"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

const w381WatchID = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"

// The built-in adapter refuses a pointer whose target and obligation name
// different subscribers, so a substituted session cannot be prompted.
func TestW381DaemonAdapterRefusesForeignObligation(t *testing.T) {
	a := &daemonWakeAdapter{api: &wake.T202API{}}
	p := wake.T210WakePointer{
		Adapter:      wake.AdapterOpenCode,
		Target:       "ses_e08f34cffffeQD2vSh7CXI3gqU",
		WatchID:      w381WatchID,
		ObligationID: "wrong:1:ses_other",
		Revision:     1,
	}
	if _, ok := a.Bind(p, nil); ok {
		t.Fatal("adapter bound a target that does not own the obligation")
	}
}

// A confirmed artifact frees the slot on the next daemon pass.
func TestAUTOSP07BurnFreesSlotOnVerifiedArtifact(t *testing.T) {
	verifier := orchestrator.ArtifactVerifierFunc(
		func(context.Context, orchestrator.SlotToken, orchestrator.SlotArtifact) (bool, error) {
			return true, nil
		})
	gate := newDaemonWakeGate(nil, verifier)
	w := orchestrator.DaemonWatch{ID: "watch:autosp07", PR: 42}
	gate.open(w, 1)
	if !gate.ledger.IsLive(w.ID, 1) {
		t.Fatal("slot token is not live after wake")
	}
	gate.burn(context.Background())
	if gate.ledger.IsLive(w.ID, 1) {
		t.Fatal("verified artifact left the slot occupied")
	}
	if len(gate.tokens) != 0 {
		t.Fatalf("retained tokens = %d, want 0", len(gate.tokens))
	}
}

// A missing artifact keeps the slot so a later pass can retry.
func TestAUTOSP07BurnKeepsSlotWhenArtifactMissing(t *testing.T) {
	verifier := orchestrator.ArtifactVerifierFunc(
		func(context.Context, orchestrator.SlotToken, orchestrator.SlotArtifact) (bool, error) {
			return false, nil
		})
	gate := newDaemonWakeGate(nil, verifier)
	w := orchestrator.DaemonWatch{ID: "watch:autosp07-missing", PR: 42}
	gate.open(w, 3)
	gate.burn(context.Background())
	if !gate.ledger.IsLive(w.ID, 3) {
		t.Fatal("missing artifact freed the slot")
	}
	if len(gate.tokens) != 1 {
		t.Fatalf("retained tokens = %d, want 1", len(gate.tokens))
	}
}

// A verifier error leaves the token live instead of burning the slot.
func TestAUTOSP07BurnKeepsSlotOnVerifierError(t *testing.T) {
	verifier := orchestrator.ArtifactVerifierFunc(
		func(context.Context, orchestrator.SlotToken, orchestrator.SlotArtifact) (bool, error) {
			return false, errors.New("artifact lookup failed")
		})
	gate := newDaemonWakeGate(nil, verifier)
	w := orchestrator.DaemonWatch{ID: "watch:autosp07-error", PR: 42}
	gate.open(w, 5)
	gate.burn(context.Background())
	if !gate.ledger.IsLive(w.ID, 5) {
		t.Fatal("verifier error freed the slot")
	}
}
