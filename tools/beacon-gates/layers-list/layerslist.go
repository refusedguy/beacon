// Package layerslist implements the go-list pass of the layer gate.
package layerslist

import (
	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func analyse(root string) ([]gate.Finding, error) {
	m, err := gate.LoadMatrix()
	if err != nil {
		return nil, err
	}
	edges, pkgs, err := gate.ListEdges(root, m)
	if err != nil {
		return nil, err
	}
	return gate.CheckMatrix(m, edges, pkgs), nil
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
