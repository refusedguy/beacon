package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	wtinfra "github.com/refusedguy/beacon/internal/infra/worktree"
)

// agentsServiceHint is printed when the background OpenCode service has not
// registered itself. Beacon never spawns the service (ADR 0029).
const agentsServiceHint = "фоновый сервис не запущен: opencode service start"

// agentRow is one agent printed by beacon agents.
type agentRow struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Mode      string         `json:"mode"`
	Stale     []staleTaskRef `json:"stale,omitempty"`
	Attention bool           `json:"attention,omitempty"`
}

// staleTask is a task in work (in_progress) whose lease has expired and has
// not been invalidated or reclaimed. The STALE column only displays it: reading
// this state never changes the lease, invalidates it or starts a capture
// (ADR 0043 p5). Fencing T3.51 and the three-signal death detection of ADR 0042
// p2 remain the only mechanisms that invalidate and reclaim leases.
type staleTask struct {
	TaskID    string    `json:"taskID"`
	Agent     string    `json:"agent"`
	ExpiredAt time.Time `json:"expiredAt"`
}

// staleTaskRef is the rendered form of one stale task in a row. AgeSeconds is
// the time since the lease expired, not since the task was created.
type staleTaskRef struct {
	TaskID     string `json:"taskID"`
	AgeSeconds int64  `json:"ageSeconds"`
}

// staleTasksReadFile is replaceable in tests. Beacon only reads the stale-task
// snapshot from its own state; the STALE column has no write capability.
var staleTasksReadFile = os.ReadFile

const staleTasksFile = "stale-tasks.json"

// staleTasksState is the on-disk snapshot of tasks in work without a live
// lease. It is produced outside this command; agents only reads it.
type staleTasksState struct {
	Tasks []staleTask `json:"tasks"`
}

// readStaleTasks is replaceable in tests, like readServiceFile above. It
// returns a snapshot of tasks whose lease expired and were not reclaimed. A
// missing snapshot means no stale work.
var readStaleTasks = func(now time.Time) ([]staleTask, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, staleTasksFile)
	data, err := staleTasksReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: не удалось прочитать снимок мёртвых аренд: %w", path, err)
	}
	var st staleTasksState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%s: неверный снимок мёртвых аренд: %w", path, err)
	}
	return st.Tasks, nil
}

// attachStaleTasks fills the read-only STALE column of each agent row. Stale
// tasks are matched to an agent by its ID or name and sorted by task ID so the
// output is stable. Nothing here mutates a lease.
func attachStaleTasks(rows []agentRow, stales []staleTask, now time.Time) {
	for i := range rows {
		refs := make([]staleTaskRef, 0)
		for _, s := range stales {
			if s.Agent != rows[i].ID && s.Agent != rows[i].Name {
				continue
			}
			age := now.Sub(s.ExpiredAt)
			if age < 0 {
				age = 0
			}
			refs = append(refs, staleTaskRef{TaskID: s.TaskID, AgeSeconds: int64(age / time.Second)})
		}
		sort.Slice(refs, func(a, b int) bool { return refs[a].TaskID < refs[b].TaskID })
		if len(refs) > 0 {
			rows[i].Stale = refs
		}
	}
}

// formatStaleAge renders an age since lease expiry for the table column.
func formatStaleAge(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%dh%dm", seconds/3600, (seconds%3600)/60)
}

// subagentModes are the OpenCode modes that can run as child sessions. Only
// these reach the CLI; the primary mode belongs to the interactive agent.
// See docs/design/opencode-cli-manager.md:41 and :62.
var subagentModes = map[string]bool{"subagent": true, "all": true}

// readServiceFile is replaceable in tests. Beacon only reads the registration
// file of the already running service, it never starts one (ADR 0029).
var readServiceFile = os.ReadFile

// readServiceRegistration confirms that the background service is running.
// The registration file is read by opencode.DiscoverWith, the single reader in
// the infra package; readServiceFile stays here only as the injectable seam
// used by tests (ADR 0092).
func readServiceRegistration() error {
	_, err := opencode.DiscoverWith(readServiceFile)
	return err
}

// runAgents implements beacon agents [--format table|json] [--route]
// [--handoff task --to session]. ADR 0058 п6 / T3.133.
func (c *CLI) runAgents(args []string) int {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода")
	route := fs.Bool("route", false, "показать агента, просящего внимания")
	handoff := fs.String("handoff", "", "передать задачу в другую сессию")
	to := fs.String("to", "", "сессия-получатель для --handoff")
	task := fs.String("task", "", "задача для привязки репозитория (T3.158)")
	repo := fs.String("repo", "", "репозиторий мультирепо-задачи (T3.158)")
	worktree := fs.String("worktree", "", "путь отдельного worktree (T3.158)")
	if !parseFlags("agents", args, c.errOut, fs) {
		return 2
	}
	if !parseFormatFlag(*format, "beacon agents: неизвестный формат %q, ожидается table или json\n", c.errOut, true) {
		return 2
	}
	if *handoff != "" || *to != "" {
		if *route {
			fmt.Fprintln(c.errOut, "beacon agents: --handoff и --route несовместимы")
			return 2
		}
		return c.runHandoff(*handoff, *to)
	}
	if *task != "" || *repo != "" || *worktree != "" {
		if *route {
			fmt.Fprintln(c.errOut, "beacon agents: привязка репозитория и --route несовместимы")
			return 2
		}
		if *task == "" || *repo == "" {
			fmt.Fprintln(c.errOut, "beacon agents: привязка репозитория требует --task <taskID> и --repo <путь>")
			return 2
		}
		return c.runBindRepo(*task, *repo, *worktree)
	}

	if err := readServiceRegistration(); err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: %s\n", agentsServiceHint)
		fmt.Fprintf(c.errOut, "  причина: %v\n", err)
		return 2
	}

	var err error
	rows, fromHTTP := agentRowsOverHTTP()
	if !fromHTTP {
		// Honest fallback for builds with no registered HTTP port: the legacy
		// `opencode api` child process is used and reported through errOut.
		// opencode v2.3.0 does not know app.agents, so session.list is the
		// working source there (ADR 0060).
		res := c.run(c.root, "opencode", []string{"api", "app.agents"}, nil)
		rows, err = parseAgentRows(res.stdout)
		if res.code != 0 || err != nil || len(rows) == 0 {
			res = c.run(c.root, "opencode", []string{"api", "session.list"}, nil)
			if res.code != 0 {
				fmt.Fprintln(c.errOut, "beacon agents: не удалось получить список агентов через opencode api session.list")
				if msg := strings.TrimSpace(string(res.stderr)); msg != "" {
					fmt.Fprintf(c.errOut, "  %s\n", msg)
				}
				return 2
			}
			rows, err = parseAgentRowsFromSessions(res.stdout)
			if err != nil {
				fmt.Fprintf(c.errOut, "beacon agents: не удалось разобрать ответ opencode api: %v\n", err)
				return 2
			}
		}
	}

	requests, err := readAttentionRequests()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: не удалось прочитать запросы внимания: %v\n", err)
		return 2
	}
	if *route {
		return c.routeAttention(rows, requests)
	}
	targetID, _, found := attentionTarget(rows, requests)
	if found {
		attachAttention(rows, targetID)
	}

	now := time.Now()
	stales, err := readStaleTasks(now)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: не удалось прочитать мёртвые аренды: %v\n", err)
		return 2
	}
	attachStaleTasks(rows, stales, now)

	return printAgentRows(*format, rows, c.out, c.errOut)
}

// agentRowsOverHTTP reads agents through the one read-only opencode HTTP port.
// It never spawns a child process or starts a fallback server. When no port is
// registered it returns ok=false, so runAgents can take the legacy path and
// say why (AUD-12).
func agentRowsOverHTTP() ([]agentRow, bool) {
	port, err := newReadOnlyOpencodePort()
	if err != nil || port == nil {
		return nil, false
	}
	ctx := context.Background()
	if agents, err := port.ListAgents(ctx); err == nil {
		if rows := agentRowsFromAgents(agents); len(rows) > 0 {
			return rows, true
		}
	}
	// opencode v2.3.0 answers /api/agent with an error or an empty list. The
	// session agent field is the working source there (ADR 0060).
	sessions, err := port.Sessions(ctx)
	if err != nil {
		return nil, false
	}
	return agentRowsFromSessions(sessions), true
}

// agentRowsFromAgents maps the HTTP agent list to printable rows and keeps only
// subagent/all modes, the same filter parseAgentRows applies.
func agentRowsFromAgents(agents []opencode.Agent) []agentRow {
	rows := make([]agentRow, 0, len(agents))
	for _, a := range agents {
		if !subagentModes[a.Mode] {
			continue
		}
		rows = append(rows, agentRow{ID: a.ID, Name: a.Name, Mode: a.Mode})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// distinctBy keeps the first item for each trimmed, non-empty key and preserves
// input order. Callers apply their own stable ordering to the result.
func distinctBy[T any](in []T, key func(T) string) []T {
	seen := make(map[string]bool, len(in))
	out := make([]T, 0, len(in))
	for _, item := range in {
		k := strings.TrimSpace(key(item))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item)
	}
	return out
}

// agentRowsFromSessions maps HTTP sessions to one row per distinct agent. The
// agent string is the ID and the name so attention and stale matching keep
// working the same way as with the legacy session.list path.
func agentRowsFromSessions(sessions []opencode.Session) []agentRow {
	rows := make([]agentRow, 0, len(sessions))
	for _, s := range distinctBy(sessions, func(s opencode.Session) string { return s.Agent }) {
		agent := strings.TrimSpace(s.Agent)
		rows = append(rows, agentRow{ID: agent, Name: agent})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
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

// parseAgentRowsFromSessions builds agent rows from a session.list payload.
// The payload is decoded by opencode.ParseSessions, the single decoder shared
// with the HTTP client and beacon watch (ADR 0060, ADR 0092). Every distinct
// non-empty `agent` value becomes one row; the agent string is the ID and the
// name so attention requests and stale tasks keep matching the same agent as
// they did with app.agents. Rows are sorted by ID for stable output.
func parseAgentRowsFromSessions(raw []byte) ([]agentRow, error) {
	sessions, err := opencode.ParseSessions(raw)
	if err != nil {
		return nil, err
	}
	return agentRowsFromSessions(sessions), nil
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
		fmt.Fprintln(w, "ID\tNAME\tMODE\tSTALE\tATTENTION")
		for _, r := range rows {
			cell := "-"
			if len(r.Stale) > 0 {
				parts := make([]string, 0, len(r.Stale))
				for _, s := range r.Stale {
					parts = append(parts, fmt.Sprintf("%s:%s", s.TaskID, formatStaleAge(s.AgeSeconds)))
				}
				cell = strings.Join(parts, ",")
			}
			attention := "-"
			if r.Attention {
				attention = "+"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Mode, cell, attention)
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

// sandboxWorkspaceRoots reads the workspace roots requested in an agent
// sandbox command line. A root appears as --root <path> or --root=<path>. The
// git clone argv built by sandboxCloneArgs keeps its exact shape (CCx-15).
func sandboxWorkspaceRoots(argv []string) []string {
	var roots []string
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--root" && i+1 < len(argv) {
			if root := strings.TrimSpace(argv[i+1]); root != "" {
				roots = append(roots, root)
			}
			i++
			continue
		}
		if strings.HasPrefix(arg, "--root=") {
			if root := strings.TrimSpace(strings.TrimPrefix(arg, "--root=")); root != "" {
				roots = append(roots, root)
			}
		}
	}
	return roots
}

// recordSandboxWorkspaceRoots saves the requested roots in session metadata so
// a later resume can restore them. It returns a new map; the input stays owned
// by the caller.
func recordSandboxWorkspaceRoots(metadata map[string]string, argv []string) map[string]string {
	return session.WithWorkspaceRoots(metadata, sandboxWorkspaceRoots(argv))
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
		return nil, fmt.Errorf("%s: не удалось прочитать реестр портов: %w", path, err)
	}
	var reg portRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("%s: неверный реестр портов: %w", path, err)
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
		return fmt.Errorf("%s: не удалось создать каталог состояния: %w", path, err)
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := portStateWriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("%s: не удалось записать реестр портов: %w", path, err)
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

// ---- ADR 0058 п6 / T3.133: маршрутизация внимания и handoff между сессиями ----
//
// T3.87 адресует wake к worktree. Здесь адресатом становится агент, который
// просит внимания, а задача передаётся между сессиями. Ни один из этих
// механизмов не меняет адресацию wake и не добавляет ступень в лестницу.

// attentionRequest is an explicit signal that an agent needs the manager. It
// is the input to the attention heuristic, not a wake addressed to a worktree.
type attentionRequest struct {
	Agent       string    `json:"agent"`
	TaskID      string    `json:"taskID,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	RequestedAt time.Time `json:"requestedAt"`
}

// attentionState is the on-disk list of open attention requests. It is
// produced outside this command; agents only reads it.
type attentionState struct {
	Requests []attentionRequest `json:"requests"`
}

const attentionRequestsFile = "attention-requests.json"

// attentionReadFile is replaceable in tests, like the other state seams.
var attentionReadFile = os.ReadFile

// readAttentionRequests returns the open attention requests from Beacon state.
// A missing snapshot means no agent asks for attention.
func readAttentionRequests() ([]attentionRequest, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, attentionRequestsFile)
	data, err := attentionReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: не удалось прочитать запросы внимания: %w", path, err)
	}
	var st attentionState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%s: неверный список запросов внимания: %w", path, err)
	}
	return st.Requests, nil
}

// attentionTarget applies the ADR 0058 п6 heuristic: among the agents listed
// by app.agents, pick the one with a request for attention. Requests naming an
// agent that is not in rows are ignored. The earliest request wins; ties break
// by agent ID and then task ID, so the choice is stable across runs. It returns
// the row ID of the chosen agent and the winning request.
func attentionTarget(rows []agentRow, requests []attentionRequest) (string, attentionRequest, bool) {
	known := make(map[string]string, len(rows)*2)
	for _, r := range rows {
		known[r.ID] = r.ID
		if r.Name != "" {
			known[r.Name] = r.ID
		}
	}

	var best attentionRequest
	var bestID string
	found := false
	for _, req := range requests {
		agent := strings.TrimSpace(req.Agent)
		id, ok := known[agent]
		if !ok || strings.TrimSpace(id) == "" {
			continue
		}
		if !found || req.RequestedAt.Before(best.RequestedAt) ||
			(req.RequestedAt.Equal(best.RequestedAt) &&
				(id < bestID || (id == bestID && req.TaskID < best.TaskID))) {
			best, bestID, found = req, id, true
		}
	}
	return bestID, best, found
}

// attachAttention marks the one row chosen by the heuristic.
func attachAttention(rows []agentRow, id string) {
	for i := range rows {
		rows[i].Attention = rows[i].ID == id
	}
}

// attentionRoute is the machine-readable transition target for the manager.
type attentionRoute struct {
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	TaskID    string `json:"taskID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// routeAttention prints the agent to switch to and the session its task lives
// in. A handoff moves that session, so routing follows the transfer.
func (c *CLI) routeAttention(rows []agentRow, requests []attentionRequest) int {
	id, req, ok := attentionTarget(rows, requests)
	if !ok {
		fmt.Fprintln(c.errOut, "beacon agents: нет агента с запросом внимания")
		return 2
	}
	var name string
	for _, r := range rows {
		if r.ID == id {
			name = r.Name
			break
		}
	}
	session, err := handoffSessionFor(req.TaskID)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: не удалось найти сессию задачи %s: %v\n", req.TaskID, err)
		return 2
	}
	route := attentionRoute{Agent: id, Name: name, TaskID: req.TaskID, SessionID: session, Reason: req.Reason}
	if err := json.NewEncoder(c.out).Encode(route); err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: %v\n", err)
		return 2
	}
	return 0
}

// handoffRecord is one transfer of a task to another session. ADR 0058 п6.
type handoffRecord struct {
	TaskID      string    `json:"taskID"`
	From        string    `json:"from,omitempty"`
	To          string    `json:"to"`
	HandedOffAt time.Time `json:"handedOffAt"`
}

// handoffState is the append-only journal of transfers in Beacon state.
type handoffState struct {
	Handoffs []handoffRecord `json:"handoffs"`
}

const handoffsFile = "handoffs.json"

var (
	handoffReadFile  = os.ReadFile
	handoffWriteFile = os.WriteFile
	handoffMkdirAll  = os.MkdirAll
)

func handoffsPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, handoffsFile), nil
}

// loadHandoffs reads the journal. A missing file is an empty journal; a
// corrupt one is an error, so a handoff never overwrites unknown transfers.
func loadHandoffs() (handoffState, error) {
	path, err := handoffsPath()
	if err != nil {
		return handoffState{}, err
	}
	data, err := handoffReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return handoffState{}, nil
	}
	if err != nil {
		return handoffState{}, fmt.Errorf("%s: не удалось прочитать журнал передач: %w", path, err)
	}
	var st handoffState
	if err := json.Unmarshal(data, &st); err != nil {
		return handoffState{}, fmt.Errorf("%s: неверный журнал передач: %w", path, err)
	}
	return st, nil
}

func saveHandoffs(st handoffState) error {
	path, err := handoffsPath()
	if err != nil {
		return err
	}
	if err := handoffMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%s: не создан каталог состояния: %w", path, err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return handoffWriteFile(path, append(data, '\n'), 0o600)
}

// recordHandoff transfers taskID to session to. A repeated transfer of the
// same task to the same session returns the existing record, so a retried
// command cannot duplicate the handoff.
func recordHandoff(taskID, from, to string, now time.Time) (handoffRecord, error) {
	taskID = strings.TrimSpace(taskID)
	to = strings.TrimSpace(to)
	if taskID == "" {
		return handoffRecord{}, fmt.Errorf("пустая задача")
	}
	if to == "" {
		return handoffRecord{}, fmt.Errorf("пустая сессия получателя")
	}
	st, err := loadHandoffs()
	if err != nil {
		return handoffRecord{}, err
	}
	for _, h := range st.Handoffs {
		if h.TaskID == taskID && h.To == to {
			return h, nil
		}
	}
	rec := handoffRecord{TaskID: taskID, From: from, To: to, HandedOffAt: now.UTC()}
	st.Handoffs = append(st.Handoffs, rec)
	if err := saveHandoffs(st); err != nil {
		return handoffRecord{}, err
	}
	return rec, nil
}

// handoffSessionFor returns the session a task currently lives in. A recorded
// handoff wins over the attach registry, so routing follows the transfer.
func handoffSessionFor(taskID string) (string, error) {
	if strings.TrimSpace(taskID) == "" {
		return "", nil
	}
	st, err := loadHandoffs()
	if err != nil {
		return "", err
	}
	var session string
	for _, h := range st.Handoffs {
		if h.TaskID == taskID {
			session = h.To
		}
	}
	if session != "" {
		return session, nil
	}
	return savedAttachSession(taskID)
}

// runHandoff implements beacon agents --handoff <taskID> --to <sessionID>.
// It records the transfer and prints the resulting record as JSON.
func (c *CLI) runHandoff(taskID, to string) int {
	taskID = strings.TrimSpace(taskID)
	to = strings.TrimSpace(to)
	if taskID == "" || to == "" {
		fmt.Fprintln(c.errOut, "beacon agents: --handoff требует --handoff <taskID> и --to <sessionID>")
		return 2
	}
	from, err := savedAttachSession(taskID)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: не удалось прочитать текущую сессию задачи %s: %v\n", taskID, err)
		return 2
	}
	rec, err := recordHandoff(taskID, from, to, time.Now())
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: передача задачи %s не удалась: %v\n", taskID, err)
		return 2
	}
	if err := json.NewEncoder(c.out).Encode(rec); err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: %v\n", err)
		return 2
	}
	return 0
}

// ---- ADR 0063 фича 6 / T3.158: мультирепо-задача с отдельным worktree ----
//
// Одна задача может изменять несколько репозиториев. Для каждого репозитория
// Beacon выделяет отдельный git-worktree со своей веткой и хранит состояние PR.
// Привязка идемпотентна: повторный вызов для той же пары задача/репозиторий
// возвращает существующий worktree и не дёргает git.

// repoWorktree is one repository bound to a task with its own isolated
// worktree and branch. PR is zero until one is opened; Reviewed records the
// review gate that must pass before the PR is sent (ADR 0063, фича 6).
type repoWorktree struct {
	TaskID   string    `json:"taskID"`
	Repo     string    `json:"repo"`
	Worktree string    `json:"worktree"`
	Branch   string    `json:"branch"`
	PR       int       `json:"pr,omitempty"`
	Reviewed bool      `json:"reviewed,omitempty"`
	BoundAt  time.Time `json:"boundAt"`
}

// repoWorktreeState is the on-disk registry of task/repo bindings. It lives in
// Beacon state so a binding survives a restart of the CLI.
type repoWorktreeState struct {
	Bindings []repoWorktree `json:"bindings"`
}

const (
	repoWorktreesFile = "repo-worktrees.json"
	repoWorktreesDir  = "worktrees"
)

var (
	repoWorktreeReadFile  = os.ReadFile
	repoWorktreeWriteFile = os.WriteFile
	repoWorktreeMkdirAll  = os.MkdirAll
)

func repoWorktreesPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, repoWorktreesFile), nil
}

// repoWorktreeKey derives a short, deterministic, filesystem-safe key from a
// task/repo pair. It keeps worktrees and branches distinct across tasks and
// repositories without leaking path characters into branch names.
func repoWorktreeKey(taskID, repo string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + repo))
	return fmt.Sprintf("%x", sum[:8])
}

// taskWorktreeBranch is the branch created for a task/repo binding.
func taskWorktreeBranch(taskID, repo string) string {
	return "beacon/" + repoWorktreeKey(taskID, repo)
}

// defaultRepoWorktreePath is the isolated checkout path used when a caller
// does not pass one explicitly.
func defaultRepoWorktreePath(taskID, repo string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, repoWorktreesDir, repoWorktreeKey(taskID, repo)), nil
}

// loadRepoWorktrees reads the registry. A missing file is an empty registry.
// Two bindings with the same task and repo, or the same worktree path, are
// corrupt and rejected rather than handed out again.
func loadRepoWorktrees() (repoWorktreeState, error) {
	path, err := repoWorktreesPath()
	if err != nil {
		return repoWorktreeState{}, err
	}
	data, err := repoWorktreeReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return repoWorktreeState{}, nil
	}
	if err != nil {
		return repoWorktreeState{}, fmt.Errorf("%s: не удалось прочитать реестр worktree: %w", path, err)
	}
	var st repoWorktreeState
	if err := json.Unmarshal(data, &st); err != nil {
		return repoWorktreeState{}, fmt.Errorf("%s: неверный реестр worktree: %w", path, err)
	}
	seenPair := map[string]bool{}
	seenPath := map[string]string{}
	for _, b := range st.Bindings {
		pair := b.TaskID + "\x00" + b.Repo
		if seenPair[pair] {
			return repoWorktreeState{}, fmt.Errorf("%s: задача %s и репозиторий %s привязаны дважды", path, b.TaskID, b.Repo)
		}
		seenPair[pair] = true
		if b.Worktree != "" {
			if prev, ok := seenPath[b.Worktree]; ok {
				return repoWorktreeState{}, fmt.Errorf("%s: worktree %s общий для %s и %s", path, b.Worktree, prev, pair)
			}
			seenPath[b.Worktree] = pair
		}
	}
	return st, nil
}

func saveRepoWorktrees(st repoWorktreeState) error {
	path, err := repoWorktreesPath()
	if err != nil {
		return err
	}
	if err := repoWorktreeMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%s: не создан каталог состояния: %w", path, err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return repoWorktreeWriteFile(path, append(data, '\n'), 0o600)
}

// bindingAgentID is the identity written to a worktree marker. BEACON_AGENT is
// the same source runTaskTake uses; the task ID is the fallback for callers
// that bind a worktree before claiming a card.
func bindingAgentID(taskID string) string {
	if id := strings.TrimSpace(os.Getenv("BEACON_AGENT")); id != "" {
		return id
	}
	return taskID
}

// touchBindingMarker records that an agent is using a bound worktree. The
// marker lives in Beacon state beside the registry, so the collector can read
// it without trusting the registry or scanning worktree directories. A touch
// creates the marker on first use and refreshes its timestamp afterwards.
func touchBindingMarker(worktreeDir, agentID string) error {
	store, err := stateDir()
	if err != nil {
		return err
	}
	_, err = wtinfra.TouchMarker(store, worktreeDir, agentID, time.Now().UTC())
	return err
}

// findRepoWorktree returns the binding for a task/repo pair, if any.
func findRepoWorktree(st repoWorktreeState, taskID, repo string) (repoWorktree, bool) {
	for _, b := range st.Bindings {
		if b.TaskID == taskID && b.Repo == repo {
			return b, true
		}
	}
	return repoWorktree{}, false
}

// worktreeAddArgs builds the git arguments for an isolated worktree with its
// own branch. Beacon drives git only through the injected runner (ADR 0005).
func worktreeAddArgs(branch, worktree string) []string {
	return []string{"worktree", "add", "-b", branch, worktree}
}

// fetchOriginMain refreshes origin/main through the injected runner before a
// worktree branches from it (HD-011, ADR 0306). A failed fetch stops the bind
// so the branch never starts from a stale ref.
func (c *CLI) fetchOriginMain(repo string) error {
	res := c.run(repo, "git", []string{"fetch", "origin", "main"}, nil)
	if res.code != 0 {
		msg := strings.TrimSpace(string(res.stderr))
		if msg == "" {
			msg = fmt.Sprintf("git завершился с кодом %d", res.code)
		}
		return fmt.Errorf("git fetch origin main не удался: %s", msg)
	}
	return nil
}

// bindRepoWorktree binds taskID to repo and allocates a separate worktree with
// its own branch. An existing binding is returned unchanged, so a retry cannot
// create a second checkout. An explicit worktree path lets callers place the
// checkout; an empty path uses the deterministic default under Beacon state.
func (c *CLI) bindRepoWorktree(taskID, repo, worktree string) (repoWorktree, error) {
	taskID = strings.TrimSpace(taskID)
	repo = strings.TrimSpace(repo)
	if taskID == "" {
		return repoWorktree{}, fmt.Errorf("пустая задача")
	}
	if repo == "" {
		return repoWorktree{}, fmt.Errorf("пустой репозиторий")
	}

	st, err := loadRepoWorktrees()
	if err != nil {
		return repoWorktree{}, err
	}
	if b, ok := findRepoWorktree(st, taskID, repo); ok {
		if err := touchBindingMarker(b.Worktree, bindingAgentID(taskID)); err != nil {
			return repoWorktree{}, err
		}
		return b, nil
	}

	if strings.TrimSpace(worktree) == "" {
		worktree, err = defaultRepoWorktreePath(taskID, repo)
		if err != nil {
			return repoWorktree{}, err
		}
	}
	for _, b := range st.Bindings {
		if b.Worktree == worktree {
			return repoWorktree{}, fmt.Errorf("worktree %s уже занят задачей %s", worktree, b.TaskID)
		}
	}

	if err := c.fetchOriginMain(repo); err != nil {
		return repoWorktree{}, err
	}

	branch := taskWorktreeBranch(taskID, repo)
	res := c.run(repo, "git", worktreeAddArgs(branch, worktree), nil)
	if res.code != 0 {
		msg := strings.TrimSpace(string(res.stderr))
		if msg == "" {
			msg = fmt.Sprintf("git завершился с кодом %d", res.code)
		}
		return repoWorktree{}, fmt.Errorf("git worktree add не удался: %s", msg)
	}

	b := repoWorktree{TaskID: taskID, Repo: repo, Worktree: worktree, Branch: branch, BoundAt: time.Now().UTC()}
	st.Bindings = append(st.Bindings, b)
	if err := saveRepoWorktrees(st); err != nil {
		return repoWorktree{}, err
	}
	if err := touchBindingMarker(worktree, bindingAgentID(taskID)); err != nil {
		return repoWorktree{}, err
	}
	return b, nil
}

// updateRepoWorktree mutates and persists one binding. It fails when the
// task/repo pair is unknown, so a review gate or PR never attaches to the
// wrong worktree.
func updateRepoWorktree(taskID, repo string, fn func(*repoWorktree) error) (repoWorktree, error) {
	st, err := loadRepoWorktrees()
	if err != nil {
		return repoWorktree{}, err
	}
	for i := range st.Bindings {
		if st.Bindings[i].TaskID == taskID && st.Bindings[i].Repo == repo {
			if err := fn(&st.Bindings[i]); err != nil {
				return repoWorktree{}, err
			}
			if err := saveRepoWorktrees(st); err != nil {
				return repoWorktree{}, err
			}
			if err := touchBindingMarker(st.Bindings[i].Worktree, bindingAgentID(taskID)); err != nil {
				return repoWorktree{}, err
			}
			return st.Bindings[i], nil
		}
	}
	return repoWorktree{}, fmt.Errorf("связка задачи %s и репозитория %s не найдена", taskID, repo)
}

// markRepoReviewed passes the review gate for a task/repo binding. A PR may be
// recorded only after this call (ADR 0063, фича 6).
func markRepoReviewed(taskID, repo string) (repoWorktree, error) {
	return updateRepoWorktree(taskID, repo, func(b *repoWorktree) error {
		b.Reviewed = true
		return nil
	})
}

// recordRepoPR stores the PR number for a reviewed binding. Recording a PR on
// an unreviewed binding is rejected: the review gate runs before the PR is
// sent.
func recordRepoPR(taskID, repo string, pr int) (repoWorktree, error) {
	if pr <= 0 {
		return repoWorktree{}, fmt.Errorf("неверный номер PR %d", pr)
	}
	return updateRepoWorktree(taskID, repo, func(b *repoWorktree) error {
		if !b.Reviewed {
			return fmt.Errorf("PR задачи %s и репозитория %s до review-гейта", taskID, repo)
		}
		b.PR = pr
		return nil
	})
}

// runBindRepo implements beacon agents --task <id> --repo <path> [--worktree <path>].
// It allocates the isolated worktree and prints the binding as JSON.
func (c *CLI) runBindRepo(taskID, repo, worktree string) int {
	b, err := c.bindRepoWorktree(taskID, repo, worktree)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: не удалось выделить worktree: %v\n", err)
		return 2
	}
	if err := json.NewEncoder(c.out).Encode(b); err != nil {
		fmt.Fprintf(c.errOut, "beacon agents: %v\n", err)
		return 2
	}
	return 0
}
