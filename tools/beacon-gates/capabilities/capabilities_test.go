package capabilities

import (
	"os"
	"testing"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func TestAnalysePlantedExec(t *testing.T) {
	root, err := gate.Fixture(map[string]string{"internal/domain/plant.go": plantedExec})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	fs, err := analyse(root, gate.Exceptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.HasRuleAt(fs, "CAP-EXEC-IN-DOMAIN", "internal/domain/plant.go", 3) {
		t.Fatalf("planted os/exec not reported with rule and file:line: %v", fs)
	}
}

func TestAnalyseSkipsTmp(t *testing.T) {
	root, err := gate.Fixture(map[string]string{"tmp/spike/plant.go": plantedExec})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	fs, err := analyse(root, gate.Exceptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("local spike under tmp/ was reported: %v", fs)
	}
}

func TestAnalyseNearMissInfra(t *testing.T) {
	root, err := gate.Fixture(map[string]string{"internal/infra/allowed.go": allowedExec})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	fs, err := analyse(root, gate.Exceptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("legal infra capability was reported: %v", fs)
	}
}

func TestRunExitCodes(t *testing.T) {
	bad, err := gate.Fixture(map[string]string{"internal/domain/plant.go": plantedExec})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(bad)
	if got := Run([]string{"-root", bad}); got != gate.Red {
		t.Fatalf("planted tree: got %d, want Red", got)
	}

	good, err := gate.Fixture(map[string]string{"internal/infra/allowed.go": allowedExec})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(good)
	if got := Run([]string{"-root", good}); got != gate.Green {
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
