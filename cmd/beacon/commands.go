package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

func (c *CLI) runGates(args []string) int {
	path, err := findGates(c.root)
	if err != nil {
		fmt.Fprintln(c.errOut, "beacon gates:", err)
		return 2
	}

	res := c.run(c.root, path, args, nil)
	emit(res, c.out, c.errOut)
	return res.code
}
