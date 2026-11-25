// Package planner configures the pure conflict planner with glob support.
package planner

import (
	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

type Options struct {
	ByPath       bool
	ByDependency bool
	Strict       bool
}

var _ func(Options) app.ConflictPlanner = New

func New(o Options) app.ConflictPlanner {
	return app.NewPlanner(checker(o))
}

func checker(o Options) core.OverlapChecker {
	return func(a, b core.Task) bool {
		if a.ID == b.ID {
			return true
		}
		if o.ByDependency && core.OverlapByDependencies(a, b) {
			return true
		}
		if !o.ByPath {
			return false
		}
		return core.OverlapByPaths(a, b)
	}
}
