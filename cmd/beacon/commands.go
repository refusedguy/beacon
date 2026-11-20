package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// runRules is the separate read-only rules gate. It checks the canonical rule
// sources and prints an aif-gate-result fence. It never writes to the checkout.
func (c *CLI) runRules(args []string) int {
	fs := flag.NewFlagSet("rules", flag.ContinueOnError)
	root := fs.String("root", c.root, "каталог проекта для проверки правил")
	if !parseFlags("rules", args, c.errOut, fs) {
		return 2
	}

	// grep only reads the rule sources and writes nothing. Keeping the call
	// behind the runner leaves file IO out of cmd/beacon.
	key := c.run(*root, "grep", []string{"-q", "beacon/survival-rules", "AGENTS.md"}, nil)
	contract := c.run(*root, "grep", []string{"-q", ".", "docs/RULES-CHECK-CONTRACT.md"}, nil)

	status := "pass"
	blocking := false
	exit := 0
	blockers := "[]"
	switch {
	case key.code == 1:
		status = "fail"
		blocking = true
		exit = 1
		blockers = `[{"id":"R-SURVIVAL-KEY","severity":"error","file":"AGENTS.md","summary":"AGENTS.md не задаёт стабильный ключ beacon/survival-rules"}]`
	case key.code != 0, contract.code != 0:
		status = "warn"
	}

	fmt.Fprintf(c.out, "verdict: %s\n", strings.ToUpper(status))
	fmt.Fprint(c.out, "affected files: 2\n")
	fmt.Fprintf(c.out, "blocking: %t\n", blocking)
	fmt.Fprintf(c.out,
		"```aif-gate-result\n{\"schema_version\":1,\"gate\":\"rules\",\"status\":%q,\"blocking\":%t,\"blockers\":%s,\"affected_files\":[\"AGENTS.md\",\"docs/RULES-CHECK-CONTRACT.md\"],\"suggested_next\":null}\n```\n",
		status, blocking, blockers)
	return exit
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
