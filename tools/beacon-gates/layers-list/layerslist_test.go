package layerslist

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
}

func TestRunExitCodes(t *testing.T) {
	root, err := gate.Fixture(gate.LayerFixtureFiles())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if got := Run([]string{"-root", root}); got != gate.Green {
		t.Fatalf("clean tree: got %d, want Green", got)
	}
	if got := Run([]string{"-min-findings=abc"}); got != gate.Broken {
		t.Fatalf("non-numeric flag: got %d, want Broken", got)
	}
}

func TestSelfTest(t *testing.T) {
	if got := selfTestRun(); got != gate.Green {
		t.Fatalf("self-test: got %d, want Green", got)
	}
}
