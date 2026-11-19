package layersast

import (
	"fmt"
	"os"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func checkFixture(files map[string]string, want func([]gate.Finding) bool) bool {
	root, err := gate.Fixture(files)
	if err != nil {
		fmt.Println("self-test FAILED: fixture:", err)
		return false
	}
	defer os.RemoveAll(root)

	findings, err := analyse(root)
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
	base := gate.LayerFixtureFiles()
	ok := true

	// Clean tree: both passes agree and all four permissions are used.
	ok = checkFixture(base, func(fs []gate.Finding) bool {
		return len(fs) == 0
	}) && ok

	// Planted violation: go/parser sees the forbidden import and reports its
	// own file:line.
	ok = checkFixture(gate.With(base, map[string]string{
		"internal/domain/plant.go": "package domain\n\n" +
			"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
	}), func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, "LAYER-FORBIDDEN-EDGE", "internal/domain/plant.go", 3)
	}) && ok

	// A package with no matrix row is a finding.
	ok = checkFixture(gate.With(base, map[string]string{
		"internal/newlayer/x.go": "package newlayer\n",
	}), func(fs []gate.Finding) bool {
		return gate.HasRuleAt(fs, "LAYER-NO-RULE", "internal/newlayer", 1)
	}) && ok

	// Unused permission: app stops importing infra, so the app -> infra row
	// in the matrix is left with no supporting edge.
	unused := gate.With(base, map[string]string{
		"internal/app/app.go": "package app\n",
	})
	ok = checkFixture(unused, func(fs []gate.Finding) bool {
		return len(fs) == 1 && strings.Contains(fs[0].Message, "internal/app -> internal/infra")
	}) && ok

	// Divergence: a build-ignored file is parsed by go/parser but excluded by
	// go list. The two passes disagree, which is a distinct gate error.
	ok = checkFixture(gate.With(base, map[string]string{
		"internal/domain/ignored.go": "//go:build ignore\n\npackage domain\n\n" +
			"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
	}), func(fs []gate.Finding) bool {
		return gate.HasRule(fs, "LAYER-DIVERGENCE")
	}) && ok

	if !ok {
		fmt.Println("self-test FAILED: 5 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 5 fixtures")
	return gate.Green
}
