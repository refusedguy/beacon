package batchapply

import (
	"fmt"
	"io"
)

// output owns the three text streams a run writes to. It holds no report state,
// so progress can be redirected without changing what the report records.
type output struct {
	out      io.Writer
	err      io.Writer
	progress io.Writer
}

// fillDefaults installs the production writers. Tests replace any of them on
// the runner they build before calling a writer method.
func (o *output) fillDefaults() {
	if o.out == nil {
		o.out = io.Discard
	}
	if o.err == nil {
		o.err = io.Discard
	}
	if o.progress == nil {
		o.progress = o.out
	}
}

func (o *output) printf(format string, args ...any) {
	fmt.Fprintf(o.out, format, args...)
}

func (o *output) errf(format string, args ...any) {
	fmt.Fprintf(o.err, format, args...)
}
