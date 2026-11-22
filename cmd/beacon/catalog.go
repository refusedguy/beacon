package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// commandRunner is the common signature every dispatch target adapts to. A
// closure reads the current seam variable, so Register* replacements in tests
// take effect at call time rather than being captured at registration.
type commandRunner func(c *CLI, args []string) int

// command is one entry of the catalog shared by list and what. The run field
// dispatches the command but stays out of `beacon list` output.
type command struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Usage   string `json:"usage"`
	run     commandRunner
}

// catalog is the single source of truth for the command names, summaries and
// usage strings. Both list and what read this slice.
var catalog = []command{
	{Name: "agents", Summary: "Показать агентов OpenCode в режиме subagent или all", Usage: "beacon agents [--format table|json]"},
	{Name: "attach", Summary: "Подключиться к сессии через фоновый сервис OpenCode", Usage: "beacon attach <sessionID>"},
	{Name: "board", Summary: "Показать локальную доску карточек", Usage: "beacon board [--root <dir>] [--status <status>] [--owner <owner>]"},
	{Name: "build", Summary: "Собрать воспроизводимый бинарник beacon", Usage: "beacon build"},
	{Name: "deferred", Summary: "Показать отложенные wiring-карточки", Usage: "beacon deferred [--format table|json]"},
	{Name: "doctor", Summary: "Проверить версию Go, beacon-gates и каталог состояния", Usage: "beacon doctor"},
	{Name: "gates", Summary: "Передать аргументы бинарнику beacon-gates", Usage: "beacon gates [gate] [flags]"},
	{Name: "gc", Summary: "Очистить завершённые сессии Beacon", Usage: "beacon gc [--format table|json] [--apply]"},
	{Name: "gc-worktree", Summary: "Очистить устаревшие worktree", Usage: "beacon gc-worktree [--format table|json] [--apply]"},
	{Name: "index", Summary: "Пересобрать SQLite-индекс карточек", Usage: "beacon index build [--root <dir>]"},
	{Name: "init", Summary: "Подготовить проект: хуки, шаблоны и проверка MCP", Usage: "beacon init [--root <dir>] [--force]"},
	{Name: "inspect", Summary: "Показать состояние вотч, блокеров и привязки оркестратора", Usage: "beacon inspect [<pr|watchID>] [--format table|json] [--json]"},
	{Name: "list", Summary: "Показать каталог команд", Usage: "beacon list [--format table|json]"},
	{Name: "logs", Summary: "Прочитать структурированные логи проекта", Usage: "beacon logs [--tail 100] [--watch <id>] [--pr <n>] [--errors] [--format table|json] [--json]"},
	{Name: "mcp", Summary: "Запустить MCP-сервер на stdio", Usage: "beacon mcp"},
	{Name: "reset", Summary: "Очистить состояние control plane проекта", Usage: "beacon reset --yes"},
	{Name: "task", Summary: "Работа с карточками задач", Usage: "beacon task <list|new|take> [flags]"},
	{Name: "test", Summary: "Запустить тесты проекта через go test ./...", Usage: "beacon test"},
	{Name: "watch", Summary: "Показать вотчи Beacon с вердиктами и сессиями OpenCode", Usage: "beacon watch [--interval 2s] [--format table|json]"},
	{Name: "what", Summary: "Показать описание одной команды", Usage: "beacon what <command>"},
	{Name: "why", Summary: "Показать цепочку блокировок карточки", Usage: "beacon why <task>"},
}

// commands returns the catalog entries enriched with their dispatch runner.
// catalog stays a plain slice because up.go, human.go and main.go append their
// entries in init; this helper reads the final value on every call so the
// metadata and the runner table cannot drift apart.
func commands() []command {
	out := make([]command, 0, len(catalog))
	for _, c := range catalog {
		c.run = runnerFor[c.Name]
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// writeCommandHelp prints the one help format shared by `beacon what <cmd>`
// and `<cmd> --help`. It writes to out so --help exits 0 with help on stdout
// and never falls back to the general usage text. It reports whether the name
// was found in the catalog.
func writeCommandHelp(out io.Writer, name string) bool {
	for _, c := range commands() {
		if c.Name == name {
			fmt.Fprintf(out, "name: %s\nsummary: %s\nusage: %s\n", c.Name, c.Summary, c.Usage)
			return true
		}
	}
	return false
}

func parseFlags(name string, args []string, errOut io.Writer, fs *flag.FlagSet) bool {
	fs.SetOutput(errOut)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		writeUsage(errOut)
		return false
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(errOut, "beacon %s: лишние аргументы: %v\n", name, fs.Args())
		writeUsage(errOut)
		return false
	}
	return true
}

// commandFormats is the single enumeration of --format values per command.
// Validation and the format tests read this map, so a command cannot advertise
// values that the parser rejects.
var commandFormats = map[string][]string{
	"inspect": {"table", "json"},
	"logs":    {"table", "json"},
	"metrics": {"json", "prometheus"},
}

// parseCommandFormat validates a --format value against the values allowed for
// the command. --json is a caller-side alias for json, never a separate format.
func parseCommandFormat(name, format string, errOut io.Writer, showUsage bool) bool {
	allowed := commandFormats[name]
	for _, value := range allowed {
		if format == value {
			return true
		}
	}
	fmt.Fprintf(errOut, "beacon %s: неизвестный формат %q, ожидается %s\n", name, format, strings.Join(allowed, " или "))
	if showUsage {
		writeUsage(errOut)
	}
	return false
}

func runList(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода")
	if !parseFlags("list", args, errOut, fs) {
		return taskstore.ExitValidation
	}

	switch *format {
	case "json":
		if err := json.NewEncoder(out).Encode(commands()); err != nil {
			fmt.Fprintf(errOut, "beacon list: %v\n", err)
			return taskstore.ExitStartup
		}
		return taskstore.ExitOK
	case "table":
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSUMMARY\tUSAGE")
		for _, c := range commands() {
			fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, c.Summary, c.Usage)
		}
		_ = w.Flush()
		return taskstore.ExitOK
	default:
		fmt.Fprintf(errOut, "beacon list: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(errOut)
		return taskstore.ExitValidation
	}
}

func runWhat(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("what", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {}
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		writeCommandHelp(out, "what")
		return taskstore.ExitOK
	}
	if err != nil {
		writeUsage(errOut)
		return taskstore.ExitValidation
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(errOut, "beacon what: нужна одна команда")
		writeUsage(errOut)
		return taskstore.ExitValidation
	}

	name := fs.Arg(0)
	if writeCommandHelp(out, name) {
		return taskstore.ExitOK
	}

	fmt.Fprintf(errOut, "beacon what: неизвестная команда %q\n", name)
	writeUsage(errOut)
	return taskstore.ExitValidation
}
