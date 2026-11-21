package layersast

import (
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func selfTestRun() int {
	base := gate.LayerFixtureFiles()
	list := []gate.FixtureCase{
		// Clean tree: both passes agree and all four permissions are used.
		{
			Files: base,
			Want: func(fs []gate.Finding) bool {
				return len(fs) == 0
			},
		},
		// Planted violation: go/parser sees the forbidden import and reports its
		// own file:line.
		{
			Files: gate.With(base, map[string]string{
				"internal/domain/plant.go": "package domain\n\n" +
					"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
			}),
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, "LAYER-FORBIDDEN-EDGE", "internal/domain/plant.go", 3)
			},
		},
		// A package with no matrix row is a finding.
		{
			Files: gate.With(base, map[string]string{
				"internal/newlayer/x.go": "package newlayer\n",
			}),
			Want: func(fs []gate.Finding) bool {
				return gate.HasRuleAt(fs, "LAYER-NO-RULE", "internal/newlayer", 1)
			},
		},
	}

	// Unused permission: app stops importing infra, so the app -> infra row
	// in the matrix is left with no supporting edge.
	unused := gate.With(base, map[string]string{
		"internal/app/app.go": "package app\n",
	})
	list = append(list, gate.FixtureCase{
		Files: unused,
		Want: func(fs []gate.Finding) bool {
			return len(fs) == 1 && strings.Contains(fs[0].Message, "internal/app -> internal/infra")
		},
	})

	// Divergence: a build-ignored file is parsed by go/parser but excluded by
	// go list. The two passes disagree, which is a distinct gate error.
	list = append(list, gate.FixtureCase{
		Files: gate.With(base, map[string]string{
			"internal/domain/ignored.go": "//go:build ignore\n\npackage domain\n\n" +
				"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
		}),
		Want: func(fs []gate.Finding) bool {
			return gate.HasRule(fs, "LAYER-DIVERGENCE")
		},
	})

	if !gate.RunFixtures(list, analyse, gate.RemoveFixture) {
		fmt.Println("self-test FAILED: 5 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 5 fixtures")
	return gate.Green
}
