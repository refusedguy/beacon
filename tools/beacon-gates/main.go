// Command beacon-gates is a single binary for the ADR 0005 architecture gates.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/refusedguy/beacon/tools/beacon-gates/capabilities"
	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
	layersast "github.com/refusedguy/beacon/tools/beacon-gates/layers-ast"
	layerslist "github.com/refusedguy/beacon/tools/beacon-gates/layers-list"
	"github.com/refusedguy/beacon/tools/beacon-gates/reachability"
)

type command struct {
	name     string
	summary  string
	run      func([]string) int
	selfTest func() int
}

// registry names each subcommand exactly once. Names match source directories.
var registry = []command{
	{"capabilities", "capabilities used outside internal/infra", capabilities.Run, capabilities.SelfTestRun},
	{"layers-ast", "layer edges from go/parser, cross-checked with go list", layersast.Run, layersast.SelfTestRun},
	{"layers-list", "layer edges from go list -e -deps -json ./...", layerslist.Run, layerslist.SelfTestRun},
	{"reachability", "unwired exported adapters and disabled tests", reachability.Run, reachability.SelfTestRun},
}

func lookup(name string) (command, bool) {
	for _, c := range registry {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func usage(w *os.File) {
	fmt.Fprintln(w, "beacon-gates <gate> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "exit codes: 0 green, 1 red (violation found), 2 broken (could not start)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "flags: -root <dir>, -min-findings <int>, --self-test, --help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "gates:")
	printList(w)
}

func printList(w *os.File) {
	names := make([]string, 0, len(registry))
	byName := map[string]command{}
	for _, c := range registry {
		names = append(names, c.name)
		byName[c.name] = c
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "%-24s %s\n", name, byName[name].summary)
	}
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return gate.Broken
	}
	switch args[0] {
	case "--help", "-h", "help":
		usage(os.Stdout)
		return gate.Green
	case "--list":
		printList(os.Stdout)
		return gate.Green
	}

	c, ok := lookup(args[0])
	if !ok {
		return gate.Brokenf("unknown subcommand %q", args[0])
	}
	rest := args[1:]
	if len(rest) > 0 {
		switch rest[0] {
		case "--self-test", "-self-test":
			return c.selfTest()
		case "--help", "-h":
			fmt.Printf("usage: beacon-gates %s [flags]\n%s\n", c.name, c.summary)
			return gate.Green
		}
	}
	return c.run(rest)
}

func main() {
	os.Exit(run(os.Args[1:]))
}
