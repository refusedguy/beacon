// Package main implements the beacon command line entry point.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const usageText = `beacon — команды сборки и проверки Beacon

Использование:
  beacon list [--format table|json]
  beacon what <command>
  beacon doctor
  beacon build
  beacon test
  beacon gates [gate] [flags]
  beacon task <list|new|take> [flags]
  beacon why <task>
  beacon mcp
  beacon agents [--format table|json]
  beacon watch [--interval 2s] [--format table|json]
  beacon attach <sessionID>

Каталог команд для агентов: beacon list --format json
`

// runResult holds the output and exit code of a child process.
type runResult struct {
	stdout []byte
	stderr []byte
	code   int
}

// runner starts a child process. dir is its working directory and env holds
// extra environment variables. A process that cannot start returns code 2.
type runner func(dir, name string, args, env []string) runResult

// CLI carries process dependencies so tests can replace the child process.
type CLI struct {
	out    io.Writer
	errOut io.Writer
	run    runner
	root   string
}

// taskRunner delegates to the task command in cmd/beacon/task.go (T0.7).
// RegisterTask replaces it, for example in tests.
var taskRunner = func(args []string, out, errOut io.Writer) int {
	return TaskCommand(append([]string{"task"}, args...), out, errOut)
}

// RegisterTask installs the task command implementation.
func RegisterTask(fn func(args []string, out, errOut io.Writer) int) {
	taskRunner = fn
}

func main() {
	os.Exit(newDefaultCLI().Run(os.Args[1:]))
}

func newDefaultCLI() *CLI {
	return &CLI{out: os.Stdout, errOut: os.Stderr, run: realRunner, root: findModuleRoot()}
}

// Run dispatches a command and returns its exit code.
func (c *CLI) Run(args []string) int {
	if len(args) == 0 {
		writeUsage(c.errOut)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		writeUsage(c.out)
		return 0
	case "list":
		return runList(args[1:], c.out, c.errOut)
	case "what":
		return runWhat(args[1:], c.out, c.errOut)
	case "doctor":
		return c.runDoctor(args[1:])
	case "build":
		return c.runBuild(args[1:])
	case "test":
		return c.runTest(args[1:])
	case "gates":
		return c.runGates(args[1:])
	case "task":
		return taskRunner(args[1:], c.out, c.errOut)
	case "why":
		return taskRunner(append([]string{"why"}, args[1:]...), c.out, c.errOut)
	case "mcp":
		return mcpRunner(c.errOut)
	case "agents":
		return c.runAgents(args[1:])
	case "watch":
		return watchRunner(c, args[1:])
	case "attach":
		return c.runAttach(args[1:])
	default:
		fmt.Fprintf(c.errOut, "beacon: неизвестная подкоманда %q\n", args[0])
		writeUsage(c.errOut)
		return 2
	}
}

func writeUsage(w io.Writer) {
	fmt.Fprint(w, usageText)
}

func realRunner(dir, name string, args, env []string) runResult {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if len(env) != 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return runResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), code: 0}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return runResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), code: exitErr.ExitCode()}
	}

	fmt.Fprintf(&stderr, "beacon: %s: %v\n", name, err)
	return runResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), code: 2}
}

func emit(res runResult, out, errOut io.Writer) {
	if len(res.stdout) != 0 {
		_, _ = out.Write(res.stdout)
	}
	if len(res.stderr) != 0 {
		_, _ = errOut.Write(res.stderr)
	}
}

// findModuleRoot walks up from the current directory to the nearest go.mod.
func findModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		info, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
