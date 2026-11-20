package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

func (c *CLI) runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	if !parseFlags("build", args, c.errOut, fs) {
		return 2
	}

	output := filepath.Join(c.root, "bin", "beacon")
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		fmt.Fprintf(c.errOut, "beacon build: %v\n", err)
		return 2
	}

	buildArgs := []string{
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags=-buildid= -s -w",
		"-o", output,
		"./cmd/beacon",
	}
	res := c.run(c.root, "go", buildArgs, []string{"CGO_ENABLED=0"})
	emit(res, c.out, c.errOut)
	return res.code
}

func (c *CLI) runTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	if !parseFlags("test", args, c.errOut, fs) {
		return 2
	}

	res := c.run(c.root, "go", []string{"test", "./..."}, nil)
	emit(res, c.out, c.errOut)
	return res.code
}

// rulesCatalogEntry registers the read-only rules gate with beacon list and
// beacon what. The gate itself is dispatched from runGates below.
var rulesCatalogEntry = command{
	Name:    "rules",
	Summary: "Проверить правила Beacon в режиме только для чтения",
	Usage:   "beacon gates rules [--root <dir>]",
}

func init() {
	catalog = append(catalog, rulesCatalogEntry)
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
}

// runRules is the separate read-only rules gate. It checks the canonical rule
// sources and prints an aif-gate-result fence. It never writes to the checkout.
func (c *CLI) runRules(args []string) int {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	root := fs.String("root", c.root, "каталог проекта для проверки правил")
	if !parseFlags("rules", args, c.errOut, fs) {
		return 2
	}

	result := gate.RulesCheck(*root)
	fmt.Fprint(c.out, gate.FormatRules(result))
	if result.Blocking {
		return gate.Red
	}
	return gate.Green
}

func (c *CLI) runGates(args []string) int {
	if len(args) > 0 && args[0] == "rules" {
		return c.runRules(args[1:])
	}
	path, err := findGates(c.root)
	if err != nil {
		fmt.Fprintln(c.errOut, "beacon gates:", err)
		return 2
	}

	res := c.run(c.root, path, args, nil)
	emit(res, c.out, c.errOut)
	return res.code
}
