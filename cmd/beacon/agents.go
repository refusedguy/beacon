package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// agentsServiceHint is printed when the background OpenCode service has not
// registered itself. Beacon never spawns the service (ADR 0029).
const agentsServiceHint = "фоновый сервис не запущен: opencode service start"

// agentRow is one agent printed by beacon agents.
type agentRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// subagentModes are the OpenCode modes that can run as child sessions. Only
// these reach the CLI; the primary mode belongs to the interactive agent.
// See docs/design/opencode-cli-manager.md:41 and :62.
var subagentModes = map[string]bool{"subagent": true, "all": true}

// readServiceFile is replaceable in tests. Beacon only reads the registration
// file of the already running service, it never starts one (ADR 0029).
var readServiceFile = os.ReadFile

// opencodeServiceFile returns the registration file path
// $XDG_STATE_HOME/opencode/service.json. The HOME fallback matches stateDir()
// in doctor.go.
func opencodeServiceFile() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "opencode", "service.json"), nil
}

// readServiceRegistration confirms that the background service is running by
// reading and validating its registration file. A missing, unreadable or
// malformed file means the service is not available.
func readServiceRegistration() error {
	path, err := opencodeServiceFile()
	if err != nil {
		return err
	}
	data, err := readServiceFile(path)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	var reg struct {
		URL     string `json:"url"`
		PID     int    `json:"pid"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return fmt.Errorf("%s: неверный registration-файл: %v", path, err)
	}
	return nil
}

// runAgents implements beacon agents [--format table|json].
func (c *CLI) runAgents(args []string) int {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода")
	if !parseFlags("agents", args, c.errOut, fs) {
		return 2
	}
	if *format != "table" && *format != "json" {
		fmt.Fprintf(c.errOut, "beacon agents: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(c.errOut)
		return 2
	}

	if err := readServiceRegistration(); err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: %s\n", agentsServiceHint)
		fmt.Fprintf(c.errOut, "  причина: %v\n", err)
		return 2
	}

	res := c.run(c.root, "opencode", []string{"api", "app.agents"}, nil)
	if res.code != 0 {
		fmt.Fprintln(c.errOut, "beacon agents: не удалось получить список агентов через opencode api")
		if msg := strings.TrimSpace(string(res.stderr)); msg != "" {
			fmt.Fprintf(c.errOut, "  %s\n", msg)
		}
		return 2
	}

	rows, err := parseAgentRows(res.stdout)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: не удалось разобрать ответ opencode api: %v\n", err)
		return 2
	}

	return printAgentRows(*format, rows, c.out, c.errOut)
}

// rawAgent is the shape of one entry in the app.agents response.
type rawAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// parseAgentRows decodes the app.agents response and keeps only subagent/all
// modes. It accepts both a flat array and a map of mode to agents, since the
// two shapes have appeared across opencode versions.
func parseAgentRows(data []byte) ([]agentRow, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("пустой ответ")
	}

	var agents []rawAgent
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &agents); err != nil {
			return nil, err
		}
	case '{':
		var groups map[string][]rawAgent
		if err := json.Unmarshal(trimmed, &groups); err != nil {
			return nil, err
		}
		for mode, list := range groups {
			for _, a := range list {
				if a.Mode == "" {
					a.Mode = mode
				}
				agents = append(agents, a)
			}
		}
		sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	default:
		return nil, fmt.Errorf("ожидался JSON-массив или объект")
	}

	rows := make([]agentRow, 0, len(agents))
	for _, a := range agents {
		if !subagentModes[a.Mode] {
			continue
		}
		rows = append(rows, agentRow{ID: a.ID, Name: a.Name, Mode: a.Mode})
	}
	return rows, nil
}

// printAgentRows renders the filtered rows as a table or JSON.
func printAgentRows(format string, rows []agentRow, out, errOut io.Writer) int {
	switch format {
	case "json":
		if rows == nil {
			rows = []agentRow{}
		}
		if err := json.NewEncoder(out).Encode(rows); err != nil {
			fmt.Fprintf(errOut, "beacon agents: %v\n", err)
			return 2
		}
		return 0
	case "table":
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tMODE")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.ID, r.Name, r.Mode)
		}
		if err := w.Flush(); err != nil {
			fmt.Fprintf(errOut, "beacon agents: %v\n", err)
			return 2
		}
		return 0
	default:
		fmt.Fprintf(errOut, "beacon agents: неизвестный формат %q\n", format)
		return 2
	}
}

// sandboxCloneArgs builds the git arguments for one cheap agent sandbox. The
// --reference flag makes the new clone read objects from source through
// .git/objects/info/alternates, so the object store is shared and no repository
// data is copied. git clone still gives the sandbox its own refs, index, HEAD
// and working tree, which keeps agents isolated (ADR 0033 section 3, ADR 0021).
// The same path is passed as reference and as clone source: Beacon provisions
// sandboxes from a local repository and does not fetch from a transport.
func sandboxCloneArgs(source, dest string) []string {
	return []string{"clone", "--reference", source, source, dest}
}

// provisionSandbox creates an isolated working copy at dest from source and
// reports failures to errOut. It returns a CLI exit code. Beacon only drives
// git through the injected runner; it never calls exec or copies files itself
// (ADR 0005, ADR 0021). Rust, FUSE and graft binaries are deliberately absent.
func (c *CLI) provisionSandbox(source, dest string) int {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(dest) == "" {
		fmt.Fprintln(c.errOut, "beacon: песочница: нужны путь исходного репозитория и путь песочницы")
		return 2
	}

	res := c.run(c.root, "git", sandboxCloneArgs(source, dest), nil)
	if res.code != 0 {
		msg := strings.TrimSpace(string(res.stderr))
		if msg == "" {
			msg = fmt.Sprintf("git завершился с кодом %d", res.code)
		}
		fmt.Fprintf(c.errOut, "beacon: песочница: git clone --reference не удался: %s\n", msg)
		return 2
	}
	return 0
}

// The port registry lives in Beacon state, so a port stays allocated across a
// restart until the task that owns it is released (ADR 0039 section 4). Range
// numbers follow workz: one contiguous block is enough for the 15 agents.
const (
	portRegistryBase  = 3000
	portRegistryCount = 15
	portRegistryFile  = "ports.json"
)

// portStateReadFile, portStateWriteFile and portStateMkdirAll are replaceable
// in tests. Assigning os functions to variables keeps capabilities out of the
// composition root: Beacon reads and writes its own state only through these
// seams (ADR 0005). Direct os.ReadFile/WriteFile/MkdirAll calls are forbidden
// in cmd/beacon by the capabilities gate.
var (
	portStateReadFile  = os.ReadFile
	portStateWriteFile = os.WriteFile
	portStateMkdirAll  = os.MkdirAll
)

// portRegistry maps a task ID to the port it owns. Base and Count are stored
// with the allocations so a later Beacon version cannot silently reinterpret
// an existing state file.
type portRegistry struct {
	Base  int            `json:"base"`
	Count int            `json:"count"`
	Used  map[string]int `json:"used"`
}

func newPortRegistry() *portRegistry {
	return &portRegistry{Base: portRegistryBase, Count: portRegistryCount, Used: map[string]int{}}
}

func portRegistryPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, portRegistryFile), nil
}

// loadPortRegistry reads the registry from Beacon state. A missing file is a
// fresh registry. A file that holds two tasks on one port, or a port outside
// the range, is corrupt and is rejected rather than handed out again.
func loadPortRegistry() (*portRegistry, error) {
	path, err := portRegistryPath()
	if err != nil {
		return nil, err
	}
	data, err := portStateReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newPortRegistry(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: не удалось прочитать реестр портов: %v", path, err)
	}
	var reg portRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("%s: неверный реестр портов: %v", path, err)
	}
	if reg.Base <= 0 || reg.Count <= 0 {
		return nil, fmt.Errorf("%s: неверный диапазон портов %d-%d", path, reg.Base, reg.Count)
	}
	if reg.Used == nil {
		reg.Used = map[string]int{}
	}
	seen := make(map[int]string, len(reg.Used))
	for task, port := range reg.Used {
		if port < reg.Base || port >= reg.Base+reg.Count {
			return nil, fmt.Errorf("%s: порт %d задачи %s вне диапазона", path, port, task)
		}
		if prev, ok := seen[port]; ok {
			return nil, fmt.Errorf("%s: порт %d выдан и %s, и %s", path, port, prev, task)
		}
		seen[port] = task
	}
	return &reg, nil
}

// savePortRegistry writes the registry atomically enough for a CLI: one file,
// restrictive permissions. A crash mid-write leaves a parse error, which
// loadPortRegistry reports instead of issuing a colliding port.
func savePortRegistry(reg *portRegistry) error {
	path, err := portRegistryPath()
	if err != nil {
		return err
	}
	if err := portStateMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%s: не удалось создать каталог состояния: %v", path, err)
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := portStateWriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("%s: не удалось записать реестр портов: %v", path, err)
	}
	return nil
}

// assign gives taskID an unused port from the single registry. A repeated call
// for the same task returns its existing port, so a retry cannot consume a
// second one. All ports are taken from one contiguous range.
func (r *portRegistry) assign(taskID string) (int, error) {
	if strings.TrimSpace(taskID) == "" {
		return 0, fmt.Errorf("пустой идентификатор задачи")
	}
	if port, ok := r.Used[taskID]; ok {
		return port, nil
	}
	for port := r.Base; port < r.Base+r.Count; port++ {
		taken := false
		for _, used := range r.Used {
			if used == port {
				taken = true
				break
			}
		}
		if !taken {
			r.Used[taskID] = port
			return port, nil
		}
	}
	return 0, fmt.Errorf("нет свободных портов в диапазоне %d-%d", r.Base, r.Base+r.Count-1)
}

// processKiller stops the processes listening on a port. Beacon drives it
// through the injected runner; tests replace it with a recorder.
type processKiller func(port int) error

// release stops processes on the task's port and only then marks it free. A
// failed kill leaves the port allocated, so the next task cannot take a port
// that still has a live process.
func (r *portRegistry) release(taskID string, kill processKiller) error {
	port, ok := r.Used[taskID]
	if !ok {
		return nil
	}
	if kill == nil {
		return fmt.Errorf("нет снимателя процессов для порта %d", port)
	}
	if err := kill(port); err != nil {
		return fmt.Errorf("порт %d не освобождён: процессы не сняты: %w", port, err)
	}
	delete(r.Used, taskID)
	return nil
}

// assignPort loads the registry, allocates a port and persists it in one step.
func (c *CLI) assignPort(taskID string) (int, error) {
	reg, err := loadPortRegistry()
	if err != nil {
		return 0, err
	}
	port, err := reg.assign(taskID)
	if err != nil {
		return 0, err
	}
	if err := savePortRegistry(reg); err != nil {
		return 0, err
	}
	return port, nil
}

// releasePort kills the task's processes, frees the port and persists the
// result. It saves only after release succeeds.
func (c *CLI) releasePort(taskID string, kill processKiller) error {
	reg, err := loadPortRegistry()
	if err != nil {
		return err
	}
	if err := reg.release(taskID, kill); err != nil {
		return err
	}
	return savePortRegistry(reg)
}

// portProcessKiller stops listeners on a port through the injected runner.
// fuser exits 1 when nothing listens, which counts as a successful cleanup.
func (c *CLI) portProcessKiller(port int) error {
	res := c.run(c.root, "fuser", []string{"-k", fmt.Sprintf("%d/tcp", port)}, nil)
	if res.code == 0 || res.code == 1 {
		return nil
	}
	msg := strings.TrimSpace(string(res.stderr))
	if msg == "" {
		msg = fmt.Sprintf("fuser завершился с кодом %d", res.code)
	}
	return fmt.Errorf("%s", msg)
}
