package layersast

import (
	"fmt"
	"os"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func selfTestRun() int {
	list := gate.LayerSelfTestCases()

	// Divergence: a build-ignored file is parsed by go/parser but excluded by
	// go list. The two passes disagree, which is a distinct gate error.
	list = append(list, gate.FixtureCase{
		Files: gate.With(gate.LayerFixtureFiles(), map[string]string{
			"internal/domain/ignored.go": "//go:build ignore\n\npackage domain\n\n" +
				"import _ \"" + gate.LayerFixtureModule + "/internal/infra\"\n",
		}),
		Want: func(fs []gate.Finding) bool {
			return gate.HasRule(fs, "LAYER-DIVERGENCE")
		},
	})

	cleanup := func(root string) { _ = os.RemoveAll(root) }
	if !gate.RunFixtures(list, analyse, cleanup) {
		fmt.Println("self-test FAILED: 5 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 5 fixtures")
	return gate.Green
}
