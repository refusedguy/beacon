// Package hookrun runs card lifecycle hook scripts on behalf of the app layer.
// It owns the process execution that the capability gate keeps out of app.
package hookrun

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// MatcherTimeout is the default deadline a matcher grants a hook that returns
// in the asynchronous form. It mirrors the SDK's 60-second matcher default.
const MatcherTimeout = 60 * time.Second

// AsyncHook is the deferred form of a hook response. The SDK models it with
// the async_ and asyncTimeout output fields: the hook returns at once and the
// deferred work runs under a bounded deadline instead of holding the caller.
type AsyncHook struct {
	Async        bool
	AsyncTimeout time.Duration
}

// Run executes the script at path, feeds stdin to it, and returns its combined
// stdout and stderr. A failing hook is reported through err and out, the caller
// decides how to record it.
//
// The synchronous, unbounded path is the default: with no timeout argument Run
// behaves exactly as before. An optional positive timeout bounds execution; on
// expiry the process is killed and the context error is returned.
func Run(path, stdin string, timeout ...time.Duration) ([]byte, error) {
	if len(timeout) == 0 || timeout[0] <= 0 {
		return RunContext(context.Background(), path, stdin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout[0])
	defer cancel()
	return RunContext(ctx, path, stdin)
}

// RunContext executes the script under a caller-supplied context. Cancelling
// the context kills the process and surfaces the context error to the caller.
func RunContext(ctx context.Context, path, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}
