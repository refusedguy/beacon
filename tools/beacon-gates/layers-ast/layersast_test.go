package layersast

import (
	"os"
	"testing"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func TestAnalysePlantedForbiddenEdge(t *testing.T) {
	root, err := gate.Fixture(gate.With(gate.LayerFixtureFiles(), map[string]string{
		"internal/domain/plant.go": "package domain\n\n" +
			"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	fs, err := analyse(root)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.HasRuleAt(fs, "LAYER-FORBIDDEN-EDGE", "internal/domain/plant.go", 3) {
		t.Fatalf("planted layer edge not reported with rule and file:line: %v", fs)
	}
	if gate.HasRule(fs, "LAYER-DIVERGENCE") {
		t.Fatalf("passes should agree on a normal tree: %v", fs)
	}
}

func TestAnalyseDivergence(t *testing.T) {
	root, err := gate.Fixture(gate.With(gate.LayerFixtureFiles(), map[string]string{
		"internal/domain/ignored.go": "//go:build ignore\n\npackage domain\n\n" +
			"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	fs, err := analyse(root)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.HasRule(fs, "LAYER-DIVERGENCE") {
		t.Fatalf("divergent import passes not reported: %v", fs)
	}
}

func TestSelfTest(t *testing.T) {
	if got := selfTestRun(); got != gate.Green {
		t.Fatalf("self-test: got %d, want Green", got)
	}
}
