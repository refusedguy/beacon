// Package checksource adapts required-check name providers to the core port.
package checksource

import (
	"context"
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// Config is a RequiredCheckSource backed by an explicit list of names. The
// list is an override: when it is non-empty it wins over the fallback source.
// That keeps the manual merge.required_checks behaviour identical while a
// provider can fill in the names when the operator configured none.
type Config struct {
	Explicit []string
	Fallback core.RequiredCheckSource
}

var _ core.RequiredCheckSource = Config{}

// Names returns the explicit names when set, otherwise the fallback's names.
// An empty source reports no required checks.
func (c Config) Names(ctx context.Context) ([]string, error) {
	if len(c.Explicit) > 0 {
		return append([]string(nil), c.Explicit...), nil
	}
	if c.Fallback != nil {
		return c.Fallback.Names(ctx)
	}
	return nil, nil
}

// NamesDescription names the configured source for a dry-run trace. It does
// not call the fallback, so an explain path cannot block on provider I/O just
// to describe where required checks come from.
func (c Config) NamesDescription() string {
	if len(c.Explicit) > 0 {
		return "config: " + strings.Join(c.Explicit, ", ")
	}
	if c.Fallback != nil {
		return "provider fallback"
	}
	return "none configured"
}
