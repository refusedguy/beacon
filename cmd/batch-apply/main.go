// Command batch-apply turns structured research JSON into ADR, task and eval
// documents. It is the single tool used to create a wave of cards locally;
// research agents only produce JSON, the orchestrator never reads raw reports.
// Flag parsing is the only work done here: file and subprocess access lives in
// internal/infra/batchapply.
package main

import (
	"flag"
	"os"

	"github.com/refusedguy/beacon/internal/infra/batchapply"
)

func main() {
	dir := flag.String("dir", "", "batch directory with JSON files")
	dryRun := flag.Bool("dry-run", false, "print files without writing")
	check := flag.Bool("check", false, "validate JSON only, print a compact summary")
	mergePlan := flag.Bool("merge-plan", false, "merge PRs listed in merge_decisions after verifying checks")
	quiet := flag.Bool("quiet", false, "do not list every generated file")
	commit := flag.Bool("commit", false, "git add and commit generated docs")
	flag.Parse()
	os.Exit(batchapply.Run(batchapply.Options{
		Dir:       *dir,
		DryRun:    *dryRun,
		Check:     *check,
		MergePlan: *mergePlan,
		Quiet:     *quiet,
		Commit:    *commit,
	}, os.Stdout, os.Stderr))
}
