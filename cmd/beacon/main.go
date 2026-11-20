// Package main implements the beacon command line entry point.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
  beacon watch [--live] [--interval 2s] [--format table|json]
  beacon spawn <taskID> [--agent id] [--title ...]
  beacon attach <sessionID>

Каталог команд для агентов: beacon list --format json
`

// spawnCatalogEntry describes the command this card adds to dispatch. It is
// registered below instead of in catalog.go because catalog.go is outside the
// T3.145 file ownership; sorting keeps `beacon list` in stable name order.
var spawnCatalogEntry = command{
	Name:    "spawn",
	Summary: "Создать сессию OpenCode для задачи Beacon и отправить промпт",
	Usage:   "beacon spawn <taskID> [--agent id] [--title ...]",
}

func init() {
	catalog = append(catalog, spawnCatalogEntry)
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
}

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
	// Composition root of the CLI manager: one place where the thin HTTP
	// client becomes the live source and the spawn port. No agent names or
	// domain types cross this boundary (ADR 0005, ADR 0060 п1).
	wireOpenCodePorts()
	os.Exit(newDefaultCLI().Run(os.Args[1:]))
}

// wireOpenCodePorts reads the opencode registration file and installs the
// ports that need it. A missing service.json is not fatal here: commands that
// need a port report that it is not connected with their own exit code. The
// client is the only transport; cmd/beacon never opens HTTP itself.
func wireOpenCodePorts() {
	port, err := newOpencodePort()
	if err != nil {
		return
	}
	installOpenCodePorts(port)
}

// installOpenCodePorts adapts one client to every command port it can serve.
// The live source is always installed. The spawn port is installed only when
// the client also implements sessionSpawner; the T3.144 reader currently has
// no POST routes, so the assertion is how main.go stays ready for that card
// without faking a transport here.
func installOpenCodePorts(port opencodePort) {
	SetLiveSource(newLiveOpencodeSource(port))
	if spawner, ok := port.(sessionSpawner); ok {
		SetSessionSpawner(spawner)
	}
}

// liveOpencodeSource adapts the T3.144 client to the liveSource port consumed
// by beacon watch --live. It only reshapes data: sessions become watch rows
// and SSE results become opaque events. Reconnect stays in the client.
type liveOpencodeSource struct {
	port opencodePort
}

func newLiveOpencodeSource(port opencodePort) *liveOpencodeSource {
	return &liveOpencodeSource{port: port}
}

// liveEventTypes are the SSE subscriptions named by ADR 0060 п2.
var liveEventTypes = []string{"session.status", "session.execution.started"}

// SessionList maps the client's session shape onto the watch table shape. The
// thin client has no ephemeral status field, so OpenCodeState stays empty;
// readiness still comes from the Beacon verdict (ADR 0017 и ADR 0026).
func (s *liveOpencodeSource) SessionList() ([]opencodeSession, error) {
	sessions, err := s.port.Sessions(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]opencodeSession, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, opencodeSession{
			ID:    session.ID,
			Agent: session.Agent,
		})
	}
	return out, nil
}

// Events forwards new SSE frames only. A transport error closes the channel,
// which tells runWatchLive to resubscribe and re-read the snapshot; the stream
// has no history (ADR 0025). Each call owns a child context so a resubscribe
// cancels the previous client stream instead of leaking a goroutine.
func (s *liveOpencodeSource) Events(ctx context.Context) <-chan liveEvent {
	streamCtx, cancel := context.WithCancel(ctx)
	raw := s.port.EventStream(streamCtx, liveEventTypes)
	out := make(chan liveEvent)
	go func() {
		defer close(out)
		defer cancel()
		for result := range raw {
			if result.Err != nil {
				return
			}
			data, err := json.Marshal(result.Event)
			if err != nil {
				continue
			}
			select {
			case out <- liveEvent{Type: result.Event.Type, Data: data}:
			case <-streamCtx.Done():
				return
			}
		}
	}()
	return out
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
	case "spawn":
		return c.runSpawn(args[1:])
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
