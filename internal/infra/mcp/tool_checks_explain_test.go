package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// checksExplainFixture holds the three checks named in AT-05 plus two edge
// cases: a green informational check and a required check still in progress.
func checksExplainFixture() domain.Watch {
	return domain.Watch{
		ID:       "w-explain",
		Repo:     "o/r",
		PR:       11,
		HeadSHA:  "sha",
		Status:   domain.StatusNotifying,
		Verdict:  domain.VerdictKeepWorking,
		Revision: 9,
		Checks: map[string]domain.Check{
			"ci/required-fail": {State: "FAILURE", Conclusion: "FAILURE", Required: true},
			"ci/info-fail":     {State: "FAILURE", Conclusion: "FAILURE", Required: false},
			"ci/build":         {State: "SUCCESS", Conclusion: "SUCCESS", Required: true},
			"ci/info-green":    {State: "SUCCESS", Conclusion: "SUCCESS", Required: false},
			"ci/queued":        {State: "QUEUED", Required: true},
		},
	}
}

func checksExplainByName(t *testing.T, out ChecksExplainOutput) map[string]CheckExplanation {
	t.Helper()
	byName := make(map[string]CheckExplanation, len(out.Checks))
	previous := ""
	for _, check := range out.Checks {
		if check.Name <= previous {
			t.Fatalf("checks are not in name order: %q after %q", check.Name, previous)
		}
		previous = check.Name
		byName[check.Name] = check
	}
	return byName
}

func TestChecksExplainCountsForVerdict(t *testing.T) {
	w := checksExplainFixture()
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("save watch: %v", err)
	}

	out, err := NewChecksExplainHandler(store).Explain(ChecksExplainInput{WatchID: w.ID})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if out.WatchID != w.ID || out.Revision != w.Revision || out.Verdict != string(w.Verdict) {
		t.Fatalf("watch identity = %q/%d/%q, want %q/%d/%q", out.WatchID, out.Revision, out.Verdict, w.ID, w.Revision, w.Verdict)
	}
	byName := checksExplainByName(t, out)
	if len(byName) != len(w.Checks) {
		t.Fatalf("explained %d checks, want %d", len(byName), len(w.Checks))
	}

	requiredFail := byName["ci/required-fail"]
	if !requiredFail.IsFailure || !requiredFail.CountsForVerdict || !requiredFail.Required {
		t.Errorf("required failed check = %+v, want failure that counts for the verdict", requiredFail)
	}
	if requiredFail.Explanation != checksExplainRequiredFail {
		t.Errorf("required failed explanation = %q, want %q", requiredFail.Explanation, checksExplainRequiredFail)
	}
	if requiredFail.ShipFact != "" {
		t.Errorf("failed check shipFact = %q, want empty", requiredFail.ShipFact)
	}

	infoFail := byName["ci/info-fail"]
	if !infoFail.IsFailure || infoFail.CountsForVerdict || infoFail.Required {
		t.Errorf("informational failed check = %+v, want failure that does not count", infoFail)
	}
	if infoFail.Explanation != checksExplainInformational {
		t.Errorf("informational explanation = %q, want %q", infoFail.Explanation, checksExplainInformational)
	}

	build := byName["ci/build"]
	if build.IsFailure || !build.CountsForVerdict || !build.Required {
		t.Errorf("required green check = %+v, want green check that counts", build)
	}
	if build.ShipFact != "build" {
		t.Errorf("build shipFact = %q, want build", build.ShipFact)
	}

	infoGreen := byName["ci/info-green"]
	if infoGreen.CountsForVerdict || infoGreen.ShipFact != "" {
		t.Errorf("informational green check = %+v, want no verdict effect and no ship fact", infoGreen)
	}
	if infoGreen.Explanation != checksExplainInformational {
		t.Errorf("informational green explanation = %q, want %q", infoGreen.Explanation, checksExplainInformational)
	}

	queued := byName["ci/queued"]
	if queued.CountsForVerdict || queued.IsFailure || queued.ShipFact != "" {
		t.Errorf("pending check = %+v, want no verdict effect", queued)
	}
	if queued.Explanation != checksExplainPending {
		t.Errorf("pending explanation = %q, want %q", queued.Explanation, checksExplainPending)
	}
}

func TestChecksExplainDoesNotStampReadAtOrChangeWatch(t *testing.T) {
	w := checksExplainFixture()
	store := newMemStore()
	if err := store.SaveWatch(w); err != nil {
		t.Fatalf("save watch: %v", err)
	}
	obligation := domain.Obligation{
		ID:             domain.ObligationKey(w.ID, w.Revision, "ses_test"),
		WatchID:        w.ID,
		Revision:       w.Revision,
		SubscriberID:   "ses_test",
		VerdictPointer: w.Verdict,
		StagedAt:       time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
	}
	if err := store.SaveObligation(obligation); err != nil {
		t.Fatalf("save obligation: %v", err)
	}

	if _, _, err := NewChecksExplainHandler(store).Handle(context.Background(), nil, ChecksExplainInput{WatchID: w.ID}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	gotWatch, err := store.Watch(w.ID)
	if err != nil {
		t.Fatalf("re-read watch: %v", err)
	}
	if gotWatch.Verdict != w.Verdict || gotWatch.Revision != w.Revision {
		t.Errorf("watch changed: verdict=%q revision=%d, want %q/%d", gotWatch.Verdict, gotWatch.Revision, w.Verdict, w.Revision)
	}
	gotObligation, err := store.Obligation(obligation.ID)
	if err != nil {
		t.Fatalf("re-read obligation: %v", err)
	}
	if !gotObligation.ReadAt.IsZero() {
		t.Errorf("readAt = %v, want zero", gotObligation.ReadAt)
	}
}

func TestChecksExplainRejectsMissingWatch(t *testing.T) {
	handler := NewChecksExplainHandler(newMemStore())
	if _, err := handler.Explain(ChecksExplainInput{}); err == nil {
		t.Error("empty watchID: want error")
	}
	if _, err := handler.Explain(ChecksExplainInput{WatchID: "missing"}); err == nil {
		t.Error("unknown watch: want error")
	}
}

func TestChecksExplainOutputSchema(t *testing.T) {
	schema, err := ChecksExplainOutputSchema()
	if err != nil {
		t.Fatalf("output schema: %v", err)
	}
	if schema == nil || schema.Type != "object" {
		t.Fatalf("output schema = %+v, want an object schema", schema)
	}
}
