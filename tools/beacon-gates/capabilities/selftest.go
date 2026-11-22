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
	// One permanent row matches the planted finding and suppresses it. A
	// second row names a file the fixture never creates, so Apply reports it
	// as stale instead of letting the exception table rot unnoticed.
	analyseWithExceptions := func(root string) ([]gate.Finding, error) {
		ex := gate.Exceptions{Permanent: []gate.PermanentEntry{
			{Rule: "CAP-EXEC-IN-DOMAIN", Path: "internal/domain/plant.go"},
			{Rule: "CAP-EXEC-IN-DOMAIN", Path: "internal/domain/ghost.go"},
		}}
		return analyse(root, ex)
	}
	exceptionList := []gate.FixtureCase{
		{
			Files: map[string]string{
				"internal/domain/plant.go": plantedExec,
			},
			Want: func(fs []gate.Finding) bool {
				return !gate.HasRule(fs, "CAP-EXEC-IN-DOMAIN") &&
					gate.HasRule(fs, "CAP-PERMANENT-STALE")
			},
		},
	}
	cleanup := func(root string) { _ = os.RemoveAll(root) }
	if !gate.RunFixtures(list, analyseNoExceptions, cleanup) || !gate.RunFixtures(exceptionList, analyseWithExceptions, cleanup) {
		fmt.Println("self-test FAILED: 4 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 4 fixtures")
	return gate.Green
}
