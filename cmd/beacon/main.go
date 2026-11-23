// Package main implements the beacon command line entry point.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
	"gopkg.in/yaml.v3"
)

const usageText = `beacon — команды сборки и проверки Beacon

Использование:
  beacon list [--format table|json]
  beacon board [--root <dir>] [--status <status>] [--owner <owner>]  (TUI в терминале, таблица без терминала)
  beacon tui [--root <dir>] [--status <status>] [--owner <owner>]  (живой экран в терминале, снимок текстом без TTY)
  beacon what <command>
  beacon doctor [--watch] [--interval <dur>] [--max-ticks <n>] [--session <id>] -- <команда> [аргументы]
  beacon build
  beacon test
  beacon gates [gate] [flags]
  beacon task <list|new|take> [flags]
  beacon why <task>
  beacon mcp
  beacon orchestrate [--dry-run] [--format table|json]
  beacon orchestrator bind [--session <id>] [--dry-run]
  beacon orchestrator rebind --from <old> --to <new> [--dry-run]  (устарело)
  beacon agents [--format table|json]
  beacon watch [--live] [--interval 2s] [--format table|json]
  beacon spawn <taskID> [--agent id] [--title ...]
  beacon attach <sessionID>
  beacon registry list [--format table|json]
  beacon registry stop --watch <id> --revision <n> --session <id> --token <t>
  beacon registry sync [--project <key>]
  beacon registry check --config <file>
  beacon deliver <watchID>
  beacon ci check --all [--dry-run] [--repo owner/name] [--format table|json]
  beacon daemon [--interval ` + daemonIntervalDescription + `] [--once]
  beacon gc [--format table|json] [--apply] [--interval 10m]
  beacon gc-worktree [--format table|json] [--apply]
  beacon init [--root <dir>] [--force]
  beacon index build [--root <dir>]
  beacon skills-lock [--write | --check]
  beacon deferred [--format table|json]
  beacon workload [--format table|json]
  beacon metrics [--format json|prometheus]
  beacon pr create [--title ...] [--body ...] [--card T3.xxx] [--dry-run]
  beacon logs [--tail 100] [--watch <id>] [--pr <n>] [--errors] [--json]
  beacon inspect [<pr|watchID>] [--json]
  beacon regrade [--watch <id>] [--dataset <name>] [--split <name>] [--declared <a,b>] [--save <file>] [--format table|json]
  beacon human <approve|reject|rework|unblock|pause|resume|cancel> <watchID|blockerID> [--note "..."]
  beacon reset --yes

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

// serverCatalogEntry registers server in the command catalog. It lives here
// rather than in catalog.go because catalog.go is outside this card's file
// ownership; the init append keeps `beacon list` and runnerFor in sync.
var serverCatalogEntry = command{
	Name:    "server",
	Summary: "Запустить HTTP-сервер Beacon с лизингом единственного экземпляра",
	Usage:   "beacon server",
}

// regradeCatalogEntry registers the artifact-only verdict recomputation. It
// lives here because catalog.go is outside this card's file ownership; the
// append keeps `beacon list` in stable name order.
var regradeCatalogEntry = command{
	Name:    "regrade",
	Summary: "Пересчитать вердикт по сохранённым доказательствам Watch без повторного прогона",
	Usage:   "beacon regrade [--watch <id>] [--dataset <name>] [--split <name>] [--declared <a,b>] [--save <file>] [--format table|json]",
}

// skillsLockCatalogEntry registers the lock generator in the command catalog.
// It lives here because catalog.go is outside this card's file ownership.
var skillsLockCatalogEntry = command{
	Name:    "skills-lock",
	Summary: "Пересчитать или проверить skills-lock.json по снимкам скиллов",
	Usage:   "beacon skills-lock [--write | --check]",
}

func init() {
	catalog = append(catalog, spawnCatalogEntry, serverCatalogEntry, regradeCatalogEntry, skillsLockCatalogEntry)
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })

	// commandFormats lives in catalog.go, outside this card's file ownership.
	// Registering the value here keeps --format validation and the catalog
	// format test in sync with the regrade entry above.
	commandFormats["regrade"] = []string{"table", "json"}

	// RegisterGC and RegisterGCWorktree are installed in gc.go, and
	// RegisterBoard in board_wire.go. The two commands owned by deferred cards
	// without their own init are installed here: the dispatch cases below
	// call the registered seams, never the raw runners.
	RegisterInit(runInit)
	RegisterIndex(runIndex)
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
	cleanupObserve := setupObserve(os.Args[1:]...)
	wireOpenCodePorts()
	code := newDefaultCLI().Run(os.Args[1:])
	cleanupObserve()
	os.Exit(code)
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
	SetOpenCodePort(port)
}

// installOpenCodePorts adapts one client to every command port it can serve.
// The live source is always installed. The spawn and delivery ports are
// installed by type assertion against the narrow interfaces in spawn.go,
// deliver.go and opencode.go; the T3.144 reader has no POST routes and no
// fallback starter, so those ports stay honestly unwired until T3.150 and
// T3.151 merge. No call to a method that does not exist yet is compiled here.
func installOpenCodePorts(port opencodeReader) {
	SetLiveSource(newLiveOpencodeSource(port))
}

// liveOpencodeSource adapts the T3.144 client to the liveSource port consumed
// by beacon watch --live. It only reshapes data: sessions become watch rows
// and SSE results become opaque events. Reconnect stays in the client.
type liveOpencodeSource struct {
	port opencodeReader
}

func newLiveOpencodeSource(port opencodeReader) *liveOpencodeSource {
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

// Run dispatches a command and returns its exit code. The dispatch is logged in
// the same file as the cycle events, so a failed command is never invisible.
func (c *CLI) Run(args []string) int {
	if len(args) == 0 {
		writeUsage(c.errOut)
		return 2
	}
	start := time.Now()
	code := c.dispatch(args)
	observe.Command(args[0], args[1:])
	level := observe.LevelInfo
	if code != 0 {
		level = observe.LevelWarn
	}
	observe.Event(level, "command_done", observe.Fields{
		observe.FieldCommand: args[0], "code": code, observe.FieldDuration: time.Since(start).String(),
	})
	return code
}

// runnerFor is the single name -> runner table for the CLI. Every dispatchable
// command is here, including the ones hidden from `beacon list`. Each entry
// adapts the varied runner signatures to commandRunner and reads the current
// seam, so Register* replacements in tests are honoured at call time.
var runnerFor map[string]commandRunner

func init() {
	runnerFor = map[string]commandRunner{
		"list":         func(c *CLI, args []string) int { return runList(args, c.out, c.errOut) },
		"board":        func(c *CLI, args []string) int { return boardRunner(c, args) },
		"tui":          func(c *CLI, args []string) int { return tuiRunner(c, args) },
		"what":         func(c *CLI, args []string) int { return runWhat(args, c.out, c.errOut) },
		"doctor":       func(c *CLI, args []string) int { return c.runDoctor(args) },
		"build":        func(c *CLI, args []string) int { return c.runBuild(args) },
		"test":         func(c *CLI, args []string) int { return c.runTest(args) },
		"gates":        func(c *CLI, args []string) int { return c.runGates(args) },
		"task":         func(c *CLI, args []string) int { return taskRunner(args, c.out, c.errOut) },
		"why":          func(c *CLI, args []string) int { return taskRunner(append([]string{"why"}, args...), c.out, c.errOut) },
		"mcp":          func(c *CLI, args []string) int { return mcpRunner(c.errOut) },
		"server":       func(c *CLI, args []string) int { return serverRunner(c.errOut) },
		"orchestrate":  func(c *CLI, args []string) int { return orchestratorRunner(c, args) },
		"orchestrator": func(c *CLI, args []string) int { return orchestratorCommandRunner(c, args) },
		"agents":       func(c *CLI, args []string) int { return c.runAgents(args) },
		"watch":        func(c *CLI, args []string) int { return watchRunner(c, args) },
		"spawn":        func(c *CLI, args []string) int { return c.runSpawn(args) },
		"attach":       func(c *CLI, args []string) int { return c.runAttach(args) },
		"registry":     func(c *CLI, args []string) int { return c.registryCommand(args) },
		"deliver":      func(c *CLI, args []string) int { return deliverRunner(c, args) },
		"ci":           func(c *CLI, args []string) int { return ciRunner(c, args) },
		"daemon":       func(c *CLI, args []string) int { return daemonRunner(c, args) },
		"gc":           func(c *CLI, args []string) int { return gcRunner(c, args) },
		"gc-worktree":  func(c *CLI, args []string) int { return gcWorktreeRunner(c, args) },
		"init":         func(c *CLI, args []string) int { return initRunner(c, args) },
		"index":        func(c *CLI, args []string) int { return indexRunner(c, args) },
		"skills-lock":  func(c *CLI, args []string) int { return c.runSkillsLock(args) },
		"deferred":     func(c *CLI, args []string) int { return deferredRunner(c, args) },
		"workload":     func(c *CLI, args []string) int { return workloadRunner(c, args) },
		"metrics":      func(c *CLI, args []string) int { return metricsRunner(c, args) },
		"pr":           func(c *CLI, args []string) int { return prcreateRunner(c, args) },
		"logs":         func(c *CLI, args []string) int { return logsRunner(c, args) },
		"inspect":      func(c *CLI, args []string) int { return inspectRunner(c, args) },
		"regrade":      func(c *CLI, args []string) int { return regradeRunner(c, args) },
		"human":        func(c *CLI, args []string) int { return humanRunner(c, args) },
		"reset":        func(c *CLI, args []string) int { return resetRunner(c, args) },
		"up":           func(c *CLI, args []string) int { return upRunner(c, args) },
	}
}

// registryRunner holds the registry command implementation. The stop path
// returns a verdict and hands it to printRegistryStopVerdict, so the CLI never
// prints success before the stopped state is saved (ADR 0091).
var registryRunner = func(c *CLI, args []string) int { return runRegistry(c, args) }

// RegisterRegistry installs the registry command implementation, mirroring
// RegisterWatch and RegisterTask.
func RegisterRegistry(fn func(*CLI, []string) int) { registryRunner = fn }

// printRegistryStopVerdict turns one checked-stop verdict into CLI output and
// an exit code. A refusal and an uncertain stop are different answers. Only a
// saved state prints the allowed and stopped lines, and they print together.
func printRegistryStopVerdict(c *CLI, verdict registryStopVerdict) int {
	switch verdict.Outcome {
	case stopOutcomeStopped:
		want := verdict.Selection
		fmt.Fprintf(c.out, "stop_allowed watch=%s revision=%d session=%s\n",
			want.WatchID, want.Revision, want.SessionID)
		fmt.Fprintf(c.out, "stopped watch=%s revision=%d session=%s\n",
			want.WatchID, want.Revision, want.SessionID)
		return 0
	case stopOutcomeRefused:
		fmt.Fprintf(c.out, "stop_refused reason=%s detail=%s\n", verdict.Reason, verdict.Detail)
		return 1
	case stopOutcomeUncertain:
		fmt.Fprintf(c.out, "stop_uncertain stopPhase=%s reason=%s detail=%s\n",
			verdict.StopPhase, verdict.Reason, verdict.Detail)
		return 2
	default:
		return 2
	}
}

func (c *CLI) dispatch(args []string) int {
	switch args[0] {
	case "-h", "--help", "help":
		writeUsage(c.out)
		return 0
	}
	if run, ok := runnerFor[args[0]]; ok {
		if hasHelpFlag(args[1:]) {
			if !writeCommandHelp(c.out, args[0]) {
				// Hidden commands have no catalog entry, but still owe the
				// caller help on stdout with a zero exit code.
				fmt.Fprintf(c.out, "name: %s\nusage: beacon %s [flags]\n", args[0], args[0])
			}
			return 0
		}
		return run(c, args[1:])
	}
	fmt.Fprintf(c.errOut, "beacon: неизвестная подкоманда %q\n", args[0])
	writeUsage(c.errOut)
	return 2
}

// hasHelpFlag reports whether the command arguments ask for help. It is
// checked at dispatch so every command, including task and spawn whose flags
// are parsed by hand, answers --help with its own help on stdout and exit 0.
// A `--` separator hands the rest of the arguments to an external command, so
// help flags after it are data, not a request for the top-level command help.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// registryCommand routes the registry subcommands. The list and check
// subcommands are wired here (T5.53, T5.63) while stop and sync stay in
// registry_stop.go, which other cards own.
func (c *CLI) registryCommand(args []string) int {
	if len(args) == 0 {
		return registryRunner(c, args)
	}
	switch args[0] {
	case "list":
		return registryListRunner(c, args[1:])
	case "check":
		return c.runRegistryCheck(args[1:])
	default:
		return registryRunner(c, args)
	}
}

// runRegistryCheck reads an author registry config and runs the check from
// registry_check.go. The file goes through fs.FS so cmd/beacon keeps the file
// capability inside internal/infra (ADR 0005). A decode or read failure is a
// broken run with exit 2; a config with issues exits 1.
func (c *CLI) runRegistryCheck(args []string) int {
	fsFlags := flag.NewFlagSet("registry check", flag.ContinueOnError)
	configPath := fsFlags.String("config", "", "path to the registry config file")
	if !parseFlags("registry check", args, c.errOut, fsFlags) {
		return 2
	}
	path := *configPath
	if path == "" {
		fmt.Fprintln(c.errOut, "beacon registry check: --config is required")
		return 2
	}
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	data, err := fs.ReadFile(os.DirFS(dir), name)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon registry check: read config: %v\n", err)
		return 2
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Fprintf(c.errOut, "beacon registry check: decode config: %v\n", err)
		return 2
	}
	if config == nil {
		fmt.Fprintln(c.errOut, "beacon registry check: empty config")
		return 2
	}
	return registryCheckRunner(checkRegistryConfig(config), c.out)
}

// runSkillsLock regenerates or checks skills-lock.json. Exactly one mode is
// required. --write rebuilds the lock from the skill directories on disk,
// --check exits 1 when the checked-in lock is stale.
func (c *CLI) runSkillsLock(args []string) int {
	fs := flag.NewFlagSet("skills-lock", flag.ContinueOnError)
	write := fs.Bool("write", false, "recompute and write skills-lock.json")
	check := fs.Bool("check", false, "fail if skills-lock.json is stale")
	if !parseFlags("skills-lock", args, c.errOut, fs) {
		return 2
	}
	if *write == *check {
		fmt.Fprintln(c.errOut, "beacon skills-lock: укажите ровно один флаг: --write или --check")
		return 2
	}
	if *write {
		lock, err := writeSkillsLock(c.root)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon skills-lock --write: %v\n", err)
			return 2
		}
		fmt.Fprintf(c.out, "ok write skills=%d\n", len(lock.Skills))
		return 0
	}
	findings, err := verifySkillsLock(c.root)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon skills-lock --check: %v\n", err)
		return 2
	}
	if len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintf(c.errOut, "stale %s: %s\n", finding.Name, finding.Problem)
		}
		return 1
	}
	fmt.Fprintln(c.out, "ok skills-lock актуален")
	return 0
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
