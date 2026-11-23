package observe

import (
	"errors"
	"fmt"
	"sync"
)

// ErrorCollector accumulates errors in insertion order and joins them into one
// error on Error. Errors added with Addf keep their cause through %w, so
// errors.Is and errors.As still reach it after the join.
//
// The collector is safe for concurrent use. This matters for the GitHub
// GraphQL fan-out, where several chunk goroutines append to the same
// collector. Sequential callers pay nothing for a lock they never contend.
type ErrorCollector struct {
	mu   sync.Mutex
	errs []error
}

// Add appends err. Nil errors are ignored, matching errors.Join on an empty
// set.
func (c *ErrorCollector) Add(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.errs = append(c.errs, err)
	c.mu.Unlock()
}

// Addf formats err with fmt.Errorf and appends it. Use %w to keep the cause.
func (c *ErrorCollector) Addf(format string, args ...any) {
	c.Add(fmt.Errorf(format, args...))
}

// Error returns the joined error, or nil when nothing was added. The order is
// the order of Add calls, which is the order errors.Join would receive.
func (c *ErrorCollector) Error() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(c.errs...)
}
