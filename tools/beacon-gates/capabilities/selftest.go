package capabilities

import (
	"fmt"
	"os"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

const plantedExec = "package domain\n\nimport \"os/exec\"\n\n" +
	"func Run() error { _, err := exec.LookPath(\"gh\"); return err }\n"

const allowedExec = "package infra\n\nimport \"os/exec\"\n\n" +
	"func Run() error { _, err := exec.LookPath(\"gh\"); return err }\n"

const plantedFile = "package app\n\nimport \"os\"\n\n" +
	"func Open() (*os.File, error) { return os.Open(\"x\") }\n"

func checkFixture(files map[string]string, want func([]gate.Finding) bool) bool {
	root, err := gate.Fixture(files)
	if err != nil {
		fmt.Println("self-test FAILED: fixture:", err)
		return false
	}
	defer os.RemoveAll(root)

	findings, err := analyse(root, gate.Exceptions{})
	if err != nil {
		fmt.Println("self-test FAILED: analyse:", err)
		return false
	}
	if !want(findings) {
		fmt.Printf("self-test FAILED: unexpected findings: %v\n", findings)
		return false
	}
	return true
}

func selfTestRun() int {
	ok := true

	// Positive control: planted os/exec in domain must be reported by the
	// production analyse with the exact rule and file:line.
	ok = checkFixture(map[string]string{
		"internal/domain/plant.go": plantedExec,
	}, func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, "CAP-EXEC-IN-DOMAIN", "internal/domain/plant.go", 3)
	}) && ok

	// Near-miss: the same call in infra is legal and stays green.
	ok = checkFixture(map[string]string{
		"internal/infra/allowed.go": allowedExec,
	}, func(fs []gate.Finding) bool {
		return len(fs) == 0
	}) && ok

	// Second capability: an os.Open call in app must report the call site.
	ok = checkFixture(map[string]string{
		"internal/app/open.go": plantedFile,
	}, func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, "CAP-FILE-IN-APP", "internal/app/open.go", 5)
	}) && ok

	if !ok {
		fmt.Println("self-test FAILED: 3 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 3 fixtures")
	return gate.Green
}
