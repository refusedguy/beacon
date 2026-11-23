// Package planner configures the pure conflict planner with glob support.
package planner

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

type Options struct {
	ByPath       bool
	ByDependency bool
	Strict       bool
}

func New(o Options) app.GreedyPlanner {
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
		for _, x := range a.Scope {
			for _, y := range b.Scope {
				if scopeOverlap(x, y) {
					return true
				}
			}
		}
		return false
	}
}

func kindOf(s core.ScopePath) byte {
	if len(s) > 1 {
		return s[0]
	}
	return 'f'
}

func valueOf(s core.ScopePath) string {
	if len(s) < 2 {
		return ""
	}
	return string(s[2:])
}

func match(pattern, name string) bool {
	ok, err := doublestar.PathMatch(pattern, name)
	return err == nil && ok
}

func scopeOverlap(a, b core.ScopePath) bool {
	ka, kb := kindOf(a), kindOf(b)
	va, vb := valueOf(a), valueOf(b)
	if va == "" || vb == "" {
		return false
	}
	if ka == 'a' || kb == 'a' {
		return true
	}
	if ka == 'g' && kb == 'g' {
		return match(va, vb) || match(vb, va) || commonPrefix(va, vb)
	}
	if ka == 'g' {
		return match(va, vb) || (kb == 'd' && strings.HasPrefix(va, vb+"/"))
	}
	if kb == 'g' {
		return match(vb, va) || (ka == 'd' && strings.HasPrefix(vb, va+"/"))
	}
	if ka == 'd' || kb == 'd' {
		dir, file := va, vb
		if ka != 'd' {
			dir, file = vb, va
		}
		return dir == file || strings.HasPrefix(file, dir+"/")
	}
	return va == vb
}

func commonPrefix(a, b string) bool {
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	n := len(pa)
	if len(pb) < n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		if pa[i] != pb[i] && !strings.ContainsAny(pa[i], "*?") && !strings.ContainsAny(pb[i], "*?") {
			return false
		}
	}
	return true
}
