package main

import (
	"fmt"
	"io"
)

// parseFormatFlag validates a --format value shared by the CLI commands.
// errFmt is the command-specific error text and must hold one %q for the
// rejected value. When showUsage is true the command usage follows the error.
// It returns true when the format is accepted.
func parseFormatFlag(format, errFmt string, errOut io.Writer, showUsage bool) bool {
	if format == "table" || format == "json" {
		return true
	}
	fmt.Fprintf(errOut, errFmt, format)
	if showUsage {
		writeUsage(errOut)
	}
	return false
}
