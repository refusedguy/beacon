package gate

import "fmt"

// FixtureCase is one self-test check: a synthetic file tree and a predicate
// over the findings the analyse function returns for it.
type FixtureCase struct {
	Files map[string]string
	Want  func([]Finding) bool
}

// RunFixtures builds each case in list, runs analyse on the generated tree,
// checks the findings with the case's Want, then cleans the tree with cleanup.
// It reports every failing case and returns true only when all cases pass.
// A nil cleanup falls back to RemoveFixture.
func RunFixtures(list []FixtureCase, analyse func(string) ([]Finding, error), cleanup func(string)) bool {
	if cleanup == nil {
		cleanup = RemoveFixture
	}
	ok := true
	for _, c := range list {
		root, err := Fixture(c.Files)
		if err != nil {
			fmt.Println("self-test FAILED: fixture:", err)
			ok = false
			continue
		}
		findings, err := analyse(root)
		cleanup(root)
		if err != nil {
			fmt.Println("self-test FAILED: analyse:", err)
			ok = false
			continue
		}
		if !c.Want(findings) {
			fmt.Printf("self-test FAILED: unexpected findings: %v\n", findings)
			ok = false
		}
	}
	return ok
}

// LayerFixtureModule is the module path used by synthetic layer trees.
const LayerFixtureModule = "example.com/beacon-fixture"

// LayerFixtureFiles is a synthetic tree whose four imports match exactly the
// four permissions in the layer matrix. Both layer gates use it as their
// green baseline. Subpackages are included to exercise prefix inheritance.
func LayerFixtureFiles() map[string]string {
	return map[string]string{
		"go.mod":                    "module " + LayerFixtureModule + "\n\ngo 1.26.0\n",
		"internal/domain/domain.go": "package domain\n",
		"internal/infra/infra.go": "package infra\n\n" +
			"import _ \"" + LayerFixtureModule + "/internal/domain\"\n",
		"internal/app/app.go": "package app\n\n" +
			"import _ \"" + LayerFixtureModule + "/internal/infra\"\n",
		"cmd/beacon/main.go": "package main\n\nimport (\n" +
			"\t_ \"" + LayerFixtureModule + "/internal/app\"\n" +
			"\t_ \"" + LayerFixtureModule + "/internal/infra\"\n" +
			")\n\nfunc main() {}\n",
	}
}

// With adds files to a fixture tree without mutating the source map.
func With(files map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(files)+len(extra))
	for k, v := range files {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
