package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestNewDescentStoresClaim pins what a caller can read back after starting a
// descent: the layer plus the hypothesis, falsifier, evidence and first step.
func TestNewDescentStoresClaim(t *testing.T) {
	got, err := NewDescent(DepthLocal, "the failure is in the test", "rerun with -count=2", "ci log abc")
	if err != nil {
		t.Fatalf("NewDescent returned error: %v", err)
	}
	want := Descent{
		Layer:      DepthLocal,
		Hypothesis: "the failure is in the test",
		Falsifier:  "rerun with -count=2",
		Evidence:   "ci log abc",
		Steps:      1,
	}
	if got != want {
		t.Errorf("NewDescent = %+v, want %+v", got, want)
	}
}

// TestNewDescentRejectsIncompleteClaim covers the boundary validation. A
// descent without a claim, a falsifier or evidence cannot be tested and must
// not be stored.
func TestNewDescentRejectsIncompleteClaim(t *testing.T) {
	tests := []struct {
		name       string
		layer      DepthLayer
		hypothesis string
		falsifier  string
		evidence   string
		want       error
	}{
		{"unknown layer", DepthLayer("SIDEWAYS"), "h", "f", "e", ErrDescentUnknownLayer},
		{"empty layer", "", "h", "f", "e", ErrDescentUnknownLayer},
		{"no hypothesis", DepthLocal, "", "f", "e", ErrDescentNoHypothesis},
		{"no falsifier", DepthLocal, "h", "", "e", ErrDescentNoFalsifier},
		{"no evidence", DepthLocal, "h", "f", "", ErrDescentNoEvidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDescent(tt.layer, tt.hypothesis, tt.falsifier, tt.evidence); !errors.Is(err, tt.want) {
				t.Errorf("NewDescent error = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestDescendRejectsReusedEvidence is the acceptance rule: two descents on
// the same data are rejected. Supplying new evidence lets the same caller
// move one layer deeper, and the original descent stays unchanged.
func TestDescendRejectsReusedEvidence(t *testing.T) {
	first, err := NewDescent(DepthLocal, "failure is in the test", "rerun with -count=2", "ci log abc")
	if err != nil {
		t.Fatalf("NewDescent returned error: %v", err)
	}

	if _, err := Descend(first, DepthBelow, "failure is in the dependency", "pin the dependency", "ci log abc"); !errors.Is(err, ErrDescentNoNewEvidence) {
		t.Fatalf("second descent with old evidence error = %v, want %v", err, ErrDescentNoNewEvidence)
	}

	second, err := Descend(first, DepthBelow, "failure is in the dependency", "pin the dependency", "ci log def")
	if err != nil {
		t.Fatalf("second descent with new evidence returned error: %v", err)
	}
	want := Descent{
		Layer:      DepthBelow,
		Hypothesis: "failure is in the dependency",
		Falsifier:  "pin the dependency",
		Evidence:   "ci log def",
		Steps:      2,
	}
	if second != want {
		t.Errorf("second descent = %+v, want %+v", second, want)
	}
	if first.Layer != DepthLocal || first.Steps != 1 || first.Evidence != "ci log abc" {
		t.Errorf("Descend mutated the original descent: %+v", first)
	}
}

// TestDescendMovesOneLayerAtATime pins the layer order and the single-step
// transition. Skipping a layer or descending in place is not a valid step.
func TestDescendMovesOneLayerAtATime(t *testing.T) {
	first, err := NewDescent(DepthLocal, "h", "f", "e1")
	if err != nil {
		t.Fatalf("NewDescent returned error: %v", err)
	}

	if _, err := Descend(first, DepthHousekeeping, "h2", "f2", "e2"); !errors.Is(err, ErrDescentNotDeeper) {
		t.Errorf("skipping to HOUSEKEEPING error = %v, want %v", err, ErrDescentNotDeeper)
	}
	if _, err := Descend(first, DepthLocal, "h2", "f2", "e2"); !errors.Is(err, ErrDescentNotDeeper) {
		t.Errorf("descending to the same layer error = %v, want %v", err, ErrDescentNotDeeper)
	}
	if !CanDescend(first, DepthBelow) {
		t.Error("LOCAL must allow a step to BELOW")
	}
	if CanDescend(first, DepthHousekeeping) {
		t.Error("LOCAL must not allow a step straight to HOUSEKEEPING")
	}

	below, err := Descend(first, DepthBelow, "h2", "f2", "e2")
	if err != nil {
		t.Fatalf("step to BELOW returned error: %v", err)
	}
	if !CanDescend(below, DepthHousekeeping) {
		t.Error("BELOW must allow a step to HOUSEKEEPING")
	}
	last, err := Descend(below, DepthHousekeeping, "h3", "f3", "e3")
	if err != nil {
		t.Fatalf("step to HOUSEKEEPING returned error: %v", err)
	}
	if last.Steps != 3 || last.Layer != DepthHousekeeping {
		t.Errorf("third step = %+v, want layer HOUSEKEEPING and 3 steps", last)
	}
}

// TestDescendRequiresClaim checks that every step carries its own hypothesis
// and falsifier, not just the first one.
func TestDescendRequiresClaim(t *testing.T) {
	first, err := NewDescent(DepthLocal, "h", "f", "e1")
	if err != nil {
		t.Fatalf("NewDescent returned error: %v", err)
	}
	tests := []struct {
		name       string
		hypothesis string
		falsifier  string
		evidence   string
		want       error
	}{
		{"no hypothesis", "", "f2", "e2", ErrDescentNoHypothesis},
		{"no falsifier", "h2", "", "e2", ErrDescentNoFalsifier},
		{"no evidence", "h2", "f2", "", ErrDescentNoEvidence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Descend(first, DepthBelow, tt.hypothesis, tt.falsifier, tt.evidence); !errors.Is(err, tt.want) {
				t.Errorf("Descend error = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestWatchPersistsDescent proves the decision can be stored in the task. A
// Watch with a descent round-trips it through JSON, and a Watch without one
// keeps its old wire shape.
func TestWatchPersistsDescent(t *testing.T) {
	descent, err := NewDescent(DepthBelow, "h", "f", "e")
	if err != nil {
		t.Fatalf("NewDescent returned error: %v", err)
	}
	watch := Watch{ID: "w-1", Depth: &descent}

	data, err := json.Marshal(watch)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	var restored Watch
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if restored.Depth == nil || *restored.Depth != descent {
		t.Errorf("restored depth = %+v, want %+v", restored.Depth, descent)
	}
	if err := ValidateDescent(*restored.Depth); err != nil {
		t.Errorf("restored descent failed validation: %v", err)
	}

	empty, err := json.Marshal(Watch{ID: "w-2"})
	if err != nil {
		t.Fatalf("json.Marshal of empty watch returned error: %v", err)
	}
	if strings.Contains(string(empty), `"depth"`) {
		t.Errorf("watch with no descent must omit depth, got %s", empty)
	}
}
