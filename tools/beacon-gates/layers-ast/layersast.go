// Package layersast implements the go/parser pass of the layer gate.
package layersast

import (
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func analyse(root string) ([]gate.Finding, error) {
	m, err := gate.LoadMatrix()
	if err != nil {
		return nil, err
	}
	astEdges, astPkgs, err := gate.ASTEdges(root, m)
	if err != nil {
		return nil, err
	}
	findings := gate.CheckMatrix(m, astEdges, astPkgs)

	// Independent cross-check. A disagreement with the go list pass is a
	// gate error with its own finding code, not a difference of opinions.
	listEdges, _, err := gate.ListEdges(root, m)
	if err != nil {
		return nil, err
	}
	if diff := gate.EdgeDiff(astEdges, listEdges, m); len(diff) > 0 {
		findings = append(findings, gate.Finding{
			Rule:    "LAYER-DIVERGENCE",
			Path:    ".",
			Line:    0,
			Message: "import passes disagree: " + strings.Join(diff, "; "),
		})
	}
	gate.SortFindings(findings)
	return findings, nil
}

// Run executes the gate and returns a process exit code.
func Run(args []string) int {
	opts, err := gate.ParseOptions(args)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	findings, err := analyse(opts.Root)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	return gate.Finish(findings, opts.MinFindings)
}

// SelfTestRun runs the planted-defect fixtures.
func SelfTestRun() int { return selfTestRun() }
