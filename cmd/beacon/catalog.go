package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
)

// command is one entry of the catalog shared by list and what.
type command struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Usage   string `json:"usage"`
}

// catalog is the single source of truth for the command names, summaries and
// usage strings. Both list and what read this slice.
var catalog = []command{
	{Name: "agents", Summary: "Показать агентов OpenCode в режиме subagent или all", Usage: "beacon agents [--format table|json]"},
	{Name: "attach", Summary: "Подключиться к сессии через фоновый сервис OpenCode", Usage: "beacon attach <sessionID>"},
	{Name: "build", Summary: "Собрать воспроизводимый бинарник beacon", Usage: "beacon build"},
	{Name: "deferred", Summary: "Показать отложенные wiring-карточки", Usage: "beacon deferred [--format table|json]"},
	{Name: "doctor", Summary: "Проверить версию Go, beacon-gates и каталог состояния", Usage: "beacon doctor"},
	{Name: "gates", Summary: "Передать аргументы бинарнику beacon-gates", Usage: "beacon gates [gate] [flags]"},
	{Name: "inspect", Summary: "Показать состояние вотч, блокеров и привязки оркестратора", Usage: "beacon inspect [<pr|watchID>] [--json]"},
	{Name: "list", Summary: "Показать каталог команд", Usage: "beacon list [--format table|json]"},
	{Name: "logs", Summary: "Прочитать структурированные логи проекта", Usage: "beacon logs [--tail 100] [--watch <id>] [--pr <n>] [--errors] [--json]"},
	{Name: "mcp", Summary: "Запустить MCP-сервер на stdio", Usage: "beacon mcp"},
	{Name: "reset", Summary: "Очистить состояние control plane проекта", Usage: "beacon reset --yes"},
	{Name: "task", Summary: "Работа с карточками задач", Usage: "beacon task <list|new|take> [flags]"},
	{Name: "watch", Summary: "Показать вотчи Beacon с вердиктами и сессиями OpenCode", Usage: "beacon watch [--interval 2s] [--format table|json]"},
	{Name: "what", Summary: "Показать описание одной команды", Usage: "beacon what <command>"},
	{Name: "why", Summary: "Показать цепочку блокировок карточки", Usage: "beacon why <task>"},
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

func runList(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	format := fs.String("format", "table", "формат вывода")
	if !parseFlags("list", args, errOut, fs) {
		return 2
	}

	switch *format {
	case "json":
		if err := json.NewEncoder(out).Encode(catalog); err != nil {
			fmt.Fprintf(errOut, "beacon list: %v\n", err)
			return 2
		}
		return 0
	case "table":
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSUMMARY\tUSAGE")
		for _, c := range catalog {
			fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, c.Summary, c.Usage)
		}
		_ = w.Flush()
		return 0
	default:
		fmt.Fprintf(errOut, "beacon list: неизвестный формат %q, ожидается table или json\n", *format)
		writeUsage(errOut)
		return 2
	}
}

func runWhat(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("what", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {}
	if fs.Parse(args) != nil {
		writeUsage(errOut)
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(errOut, "beacon what: нужна одна команда")
		writeUsage(errOut)
		return 2
	}

	name := fs.Arg(0)
	for _, c := range catalog {
		if c.Name == name {
			fmt.Fprintf(out, "name: %s\nsummary: %s\nusage: %s\n", c.Name, c.Summary, c.Usage)
			return 0
		}
	}

	fmt.Fprintf(errOut, "beacon what: неизвестная команда %q\n", name)
	writeUsage(errOut)
	return 2
}
