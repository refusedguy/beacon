package checksource

import (
	"context"

	"github.com/refusedguy/beacon/internal/infra/shared"
	"github.com/refusedguy/beacon/pkg/core"
)

// Workflow resolves required check names from CI workflow files under Root.
// It uses the shared parser so the names match config load. Detect is
// injectable for tests; nil uses the shared parser.
type Workflow struct {
	Root   string
	Detect func(root string) []string
}

var _ core.RequiredCheckSource = Workflow{}

// Names returns the sorted job names found in the workflow files.
func (w Workflow) Names(ctx context.Context) ([]string, error) {
	detect := w.Detect
	if detect == nil {
		detect = shared.DetectWorkflowJobNames
	}
	return detect(w.Root), nil
}
