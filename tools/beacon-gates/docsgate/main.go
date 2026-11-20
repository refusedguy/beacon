// Command docsgate runs the markdown/docs gate over docs/tasks and docs/adrs.
// It lives in its own main package so the committed beacon-gates binary is
// unaffected; CI builds it from source on every docs PR.
package main

import (
	"os"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	var baseline string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--self-test" || a == "-self-test":
			return docsSelfTestRun()
		case a == "-baseline" || a == "--baseline":
			if i+1 >= len(args) {
				return gate.Brokenf("missing value for %s", a)
			}
			i++
			baseline = args[i]
		case strings.HasPrefix(a, "-baseline=") || strings.HasPrefix(a, "--baseline="):
			baseline = strings.SplitN(a, "=", 2)[1]
		default:
			rest = append(rest, a)
		}
	}

	opts, err := gate.ParseOptions(rest)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	cfg, err := loadDocsConfig(baseline)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	fs, err := checkDocs(opts.Root, cfg)
	if err != nil {
		return gate.Brokenf("%v", err)
	}
	return gate.Finish(fs, opts.MinFindings)
}
