// Package hookrun runs card lifecycle hook scripts on behalf of the app layer.
// It owns the process execution that the capability gate keeps out of app.
package hookrun

import (
	"os/exec"
	"strings"
)

// Run executes the script at path, feeds stdin to it, and returns its combined
// stdout and stderr. A failing hook is reported through err and out, the caller
// decides how to record it.
func Run(path, stdin string) ([]byte, error) {
	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}
