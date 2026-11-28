package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

// nudgeStore hands Control a detached run and keeps a copy of every save. The
// test then reads the marker from what was saved, not from an in-place mutation
// of the fixture.
type nudgeStore struct {
	*fakeStore
	saved []core.Run
}

func (s *nudgeStore) Get(ctx context.Context, id core.RunID) (*core.Run, error) {
	run, err := s.fakeStore.Get(ctx, id)
	if err != nil || run == nil {
		return run, err
	}
	copy := *run
	return &copy, nil
}

func (s *nudgeStore) Save(ctx context.Context, run *core.Run) error {
	s.saved = append(s.saved, *run)
	return s.fakeStore.Save(ctx, run)
}

func newNudgeFixture(t *testing.T, reason string) (*nudgeStore, *fakeHarness, *Control) {
	t.Helper()
	store := &nudgeStore{fakeStore: newFakeStore()}
	run := core.NewRun("r-nudge", "T-1", time.Now().UTC())
	run.State = core.RunWaitingCI
	run.PRNumber = 42
	run.HeadSHA = "sha42"
	run.SessionID = "sess-nudge"
	run.WorkspacePath = "/ws/T-1"
	run.Branch = "beacon/T-1"
	run.Reason = reason
	run.ObservedSHA = "sha42"
	run.ObservedBaseSHA = "base42"
	run.ObservedIdentity = "identity-42"
	if err := store.fakeStore.Save(context.Background(), run); err != nil {
		t.Fatalf("save fixture: %v", err)
	}
	h := &fakeHarness{}
	return store, h, &Control{Store: store, Harness: h}
}

func TestNudgeWritesMarkerOnSavedRunAndKeepsFailureReason(t *testing.T) {
	ctx := context.Background()
	const failure = "unavailable: update branch: Variable $sha of type GitObjectID! was provided invalid value"
	store, h, c := newNudgeFixture(t, failure)

	if err := c.Nudge(ctx, "r-nudge", "please rebase first"); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saves = %d, want 1", len(store.saved))
	}

	saved := store.saved[0]
	if !strings.HasPrefix(saved.Reason, core.NudgeMarker+": ") {
		t.Fatalf("saved reason = %q, want %q prefix", saved.Reason, core.NudgeMarker)
	}
	if !strings.Contains(saved.Reason, failure) {
		t.Fatalf("saved reason = %q, lost failure reason %q", saved.Reason, failure)
	}
	if !saved.HoldsAgent() {
		t.Fatal("saved run did not hold its agent slot")
	}

	wire := core.NewWireRun(&saved)
	if !strings.HasPrefix(wire.Reason, core.NudgeMarker) || !strings.Contains(wire.Reason, failure) {
		t.Fatalf("wire reason = %q, want marker and intact failure reason", wire.Reason)
	}

	if len(h.wakes) != 1 {
		t.Fatalf("wakes = %+v, want one", h.wakes)
	}
	w := h.wakes[0]
	if w.RunID != "r-nudge" || w.Action != core.WakeGuidance || w.Excerpt != "please rebase first" {
		t.Fatalf("wake = %+v, want guidance for r-nudge", w)
	}
}

func TestNudgeSetsMarkerWhenRunHasNoReason(t *testing.T) {
	ctx := context.Background()
	store, h, c := newNudgeFixture(t, "")

	if err := c.Nudge(ctx, "r-nudge", "continue"); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if got := store.saved[0].Reason; got != core.NudgeMarker {
		t.Fatalf("reason = %q, want %q", got, core.NudgeMarker)
	}
	if len(h.wakes) != 1 {
		t.Fatalf("wakes = %+v, want one", h.wakes)
	}
}

func TestNudgeDoesNotStackMarkersOrStampObservation(t *testing.T) {
	ctx := context.Background()
	store, h, c := newNudgeFixture(t, "checks failed")

	for _, message := range []string{"first guidance", "second guidance"} {
		if err := c.Nudge(ctx, "r-nudge", message); err != nil {
			t.Fatalf("Nudge(%q): %v", message, err)
		}
	}

	saved := store.saved[len(store.saved)-1]
	if strings.Count(saved.Reason, core.NudgeMarker) != 1 {
		t.Fatalf("reason = %q, want exactly one marker", saved.Reason)
	}
	if saved.ObservedSHA != "sha42" || saved.ObservedBaseSHA != "base42" || saved.ObservedIdentity != "identity-42" {
		t.Fatalf("nudge stamped observation identity: %+v", saved)
	}
	if len(h.wakes) != 2 {
		t.Fatalf("wakes = %d, want 2", len(h.wakes))
	}
}
