package main

import (
	"bytes"
	"encoding/json"
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
