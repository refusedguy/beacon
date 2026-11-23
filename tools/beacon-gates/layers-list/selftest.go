package layerslist

import (
	"fmt"
	"os"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func selfTestRun() int {
	list := gate.LayerSelfTestCases()

	cleanup := func(root string) { _ = os.RemoveAll(root) }
	if !gate.RunFixtures(list, analyse, cleanup) {
		fmt.Println("self-test FAILED: 4 fixtures")
		return gate.Red
	}
	fmt.Println("self-test passed: 4 fixtures")
	return gate.Green
}
