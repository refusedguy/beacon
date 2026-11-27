package checksource

import (
	"context"
	"fmt"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

// Workflow resolves required check names from CI workflow files under Root.
// It uses the shared parser so the names match config load. Detect is
// injectable for tests; nil uses the shared parser. Fallback names are used
// only when detection finds none; an empty fallback makes Names fail so an
// unreadable or renamed workflow cannot open the merge gate.
type Workflow struct {
	Root     string
	Detect   func(root string) []string
	Fallback []string
}

var _ core.RequiredCheckSource = Workflow{}

// Names returns the sorted job names found in the workflow files on every
// call, so a rename takes effect on the next evaluation. It returns the
// fallback when no names are detected and an error when neither has a name.
func (w Workflow) Names(ctx context.Context) ([]string, error) {
	detect := w.Detect
	if detect == nil {
		detect = shared.DetectWorkflowJobNames
	}
	names := detect(w.Root)
	if len(names) > 0 {
		return names, nil
	}
	if len(w.Fallback) > 0 {
		return append([]string(nil), w.Fallback...), nil
	}
	return nil, fmt.Errorf("no required checks detected in workflows under %s", w.Root)
}
