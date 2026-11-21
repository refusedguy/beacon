package capabilities

import (
	"fmt"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

const plantedExec = "package domain\n\nimport \"os/exec\"\n\n" +
	"func Run() error { _, err := exec.LookPath(\"gh\"); return err }\n"

const allowedExec = "package infra\n\nimport \"os/exec\"\n\n" +
	"func Run() error { _, err := exec.LookPath(\"gh\"); return err }\n"

const plantedFile = "package app\n\nimport \"os\"\n\n" +
	"func Open() (*os.File, error) { return os.Open(\"x\") }\n"

func selfTestRun() int {
	list := []gate.FixtureCase{
		// Positive control: planted os/exec in domain must be reported by the
		// production analyse with the exact rule and file:line.
		{
			Files: map[string]string{
				"internal/domain/plant.go": plantedExec,
			},
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, "CAP-EXEC-IN-DOMAIN", "internal/domain/plant.go", 3)
			},
		},
		// Near-miss: the same call in infra is legal and stays green.
		{
			Files: map[string]string{
				"internal/infra/allowed.go": allowedExec,
			},
			Want: func(fs []gate.Finding) bool {
				return len(fs) == 0
			},
		},
		// Second capability: an os.Open call in app must report the call site.
		{
			Files: map[string]string{
				"internal/app/open.go": plantedFile,
			},
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, "CAP-FILE-IN-APP", "internal/app/open.go", 5)
			},
		},
	}

	analyseNoExceptions := func(root string) ([]gate.Finding, error) {
		return analyse(root, gate.Exceptions{})
	}
	if !gate.RunFixtures(list, analyseNoExceptions, gate.RemoveFixture) {
		fmt.Println("self-test FAILED: 3 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 3 fixtures")
	return gate.Green
}
