package layerslist

import (
	"fmt"
	"os"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func selfTestRun() int {
	base := gate.LayerFixtureFiles()
	list := []gate.FixtureCase{
		// Clean tree: all four matrix permissions are used.
		{
			Files: base,
			Want: func(fs []gate.Finding) bool {
				return len(fs) == 0
			},
		},
		// Planted violation: domain imports infra. go list sees the edge and the
		// source line is located from the package directory.
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

	// A permission nobody uses is also a finding: app no longer imports infra.
	unused := gate.With(base, map[string]string{
		"internal/app/app.go": "package app\n",
	})
	list = append(list, gate.FixtureCase{
		Files: unused,
		Want: func(fs []gate.Finding) bool {
			return len(fs) == 1 && strings.Contains(fs[0].Message, "internal/app -> internal/infra")
		},
	})

	cleanup := func(root string) { _ = os.RemoveAll(root) }
	if !gate.RunFixtures(list, analyse, cleanup) {
		fmt.Println("self-test FAILED: 4 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 4 fixtures")
	return gate.Green
}
