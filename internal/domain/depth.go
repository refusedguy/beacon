// Package domain holds the Beacon entities, status enums and port interfaces.
// Depth models the depth.look-below rule: a repair may move one layer deeper
// only when it carries a hypothesis, a falsifier and evidence that was not
// already spent on the previous descent.
package domain

import "errors"

// DepthLayer names the layer a repair decision operates at. LOCAL is the
// task's own layer, BELOW is one layer down, and HOUSEKEEPING is the shared
// infrastructure layer. The order matters: a descent moves one layer at a
// time, so each step needs its own hypothesis.
type DepthLayer string

const (
	DepthLocal        DepthLayer = "LOCAL"
	DepthBelow        DepthLayer = "BELOW"
	DepthHousekeeping DepthLayer = "HOUSEKEEPING"
)

var (
	// ErrDescentUnknownLayer means the layer is not one of the known values.
	ErrDescentUnknownLayer = errors.New("depth: unknown layer")
	// ErrDescentNoHypothesis means a descent carries no claim to test.
	ErrDescentNoHypothesis = errors.New("depth: hypothesis is required")
	// ErrDescentNoFalsifier means a descent carries no observation that would
	// disprove its hypothesis.
	ErrDescentNoFalsifier = errors.New("depth: falsifier is required")
	// ErrDescentNoEvidence means a descent carries no supporting data.
	ErrDescentNoEvidence = errors.New("depth: evidence is required")
	// ErrDescentNotDeeper means the target is not exactly one layer below the
	// current one.
	ErrDescentNotDeeper = errors.New("depth: target must be one layer deeper")
	// ErrDescentNoNewEvidence means a second descent reuses the evidence of
	// the first one. This is the "two descents without new data" rejection.
	ErrDescentNoNewEvidence = errors.New("depth: second descent requires new evidence")
)

// depthRank returns the position of a layer in the descent order and whether
// the layer is known.
func depthRank(layer DepthLayer) (int, bool) {
	switch layer {
	case DepthLocal:
		return 0, true
	case DepthBelow:
		return 1, true
	case DepthHousekeeping:
		return 2, true
	default:
		return 0, false
	}
}

// ValidDepthLayer reports whether layer is one of the known values.
func ValidDepthLayer(layer DepthLayer) bool {
	_, ok := depthRank(layer)
	return ok
}

// Descent is the depth decision stored on a task. Layer is where the repair
// currently works. Hypothesis is the claim that justifies being there.
// Falsifier is the observation that would disprove the claim. Evidence is
// the data that justified the most recent step, and Steps counts the steps
// already taken. Evidence makes a second descent without new data detectable:
// the same evidence cannot justify two steps.
type Descent struct {
	Layer      DepthLayer `json:"layer"`
	Hypothesis string     `json:"hypothesis,omitempty"`
	Falsifier  string     `json:"falsifier,omitempty"`
	Evidence   string     `json:"evidence,omitempty"`
	Steps      int        `json:"steps"`
}

// NewDescent starts a repair at layer with its first hypothesis. It rejects
// an unknown layer, an empty hypothesis, an empty falsifier and empty
// evidence, so a stored descent always carries a testable claim.
func NewDescent(layer DepthLayer, hypothesis, falsifier, evidence string) (Descent, error) {
	if !ValidDepthLayer(layer) {
		return Descent{}, ErrDescentUnknownLayer
	}
	if err := validateDescentClaim(hypothesis, falsifier, evidence); err != nil {
		return Descent{}, err
	}
	return Descent{
		Layer:      layer,
		Hypothesis: hypothesis,
		Falsifier:  falsifier,
		Evidence:   evidence,
		Steps:      1,
	}, nil
}

// Descend moves current one layer deeper. The target must be the next layer,
// and the caller must supply a hypothesis, a falsifier and evidence that
// differs from the stored evidence. Reusing the stored evidence rejects the
// second descent, which keeps a repair from growing deeper on data it already
// used. On success it returns the new descent and leaves current unchanged.
func Descend(current Descent, target DepthLayer, hypothesis, falsifier, evidence string) (Descent, error) {
	from, ok := depthRank(current.Layer)
	if !ok {
		return Descent{}, ErrDescentUnknownLayer
	}
	to, ok := depthRank(target)
	if !ok {
		return Descent{}, ErrDescentUnknownLayer
	}
	if to != from+1 {
		return Descent{}, ErrDescentNotDeeper
	}
	if err := validateDescentClaim(hypothesis, falsifier, evidence); err != nil {
		return Descent{}, err
	}
	if evidence == current.Evidence {
		return Descent{}, ErrDescentNoNewEvidence
	}
	return Descent{
		Layer:      target,
		Hypothesis: hypothesis,
		Falsifier:  falsifier,
		Evidence:   evidence,
		Steps:      current.Steps + 1,
	}, nil
}

// CanDescend reports whether target is exactly one layer below current. It
// does not check evidence, so callers can use it to decide whether to collect
// new data before calling Descend.
func CanDescend(current Descent, target DepthLayer) bool {
	from, okFrom := depthRank(current.Layer)
	to, okTo := depthRank(target)
	return okFrom && okTo && to == from+1
}

// ValidateDescent checks a descent read back from storage. A nil-shaped zero
// value is not a valid descent; callers check for an absent depth before
// calling this.
func ValidateDescent(d Descent) error {
	if !ValidDepthLayer(d.Layer) {
		return ErrDescentUnknownLayer
	}
	if err := validateDescentClaim(d.Hypothesis, d.Falsifier, d.Evidence); err != nil {
		return err
	}
	if d.Steps < 1 {
		return errors.New("depth: steps must be positive")
	}
	return nil
}

func validateDescentClaim(hypothesis, falsifier, evidence string) error {
	if hypothesis == "" {
		return ErrDescentNoHypothesis
	}
	if falsifier == "" {
		return ErrDescentNoFalsifier
	}
	if evidence == "" {
		return ErrDescentNoEvidence
	}
	return nil
}
