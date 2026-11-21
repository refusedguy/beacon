package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// logsRunner is the dispatch seam for `beacon logs`. RegisterLogs replaces it
// in tests like the other command runners.
var logsRunner = func(c *CLI, args []string) int { return runLogs(c, args) }

// RegisterLogs installs the logs command implementation.
func RegisterLogs(fn func(*CLI, []string) int) { logsRunner = fn }

// runLogs reads the structured log files under the project state directory
// through the observe port. No file I/O happens here, so the capability gate
// never sees os.ReadFile in cmd/beacon.
func runLogs(c *CLI, args []string) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	tail := fs.Int("tail", 100, "показать последние N записей (0 = все)")
	watch := fs.String("watch", "", "только записи с этим watchID")
	pr := fs.Int("pr", 0, "только записи с этим номером PR")
	errorsOnly := fs.Bool("errors", false, "только ошибки и записи с полем error")
	jsonOut := fs.Bool("json", false, "вывести каждую запись как компактный JSON-объект")
	if !parseFlags("logs", args, c.errOut, fs) {
		return 2
	}
	if *tail < 0 {
		fmt.Fprintf(c.errOut, "beacon logs: --tail не может быть отрицательным\n")
		return 2
	}

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon logs: %v\n", err)
		return 2
	}
	filter := observe.LogFilter{
		Tail:       *tail,
		WatchID:    *watch,
		PR:         *pr,
		OnlyErrors: *errorsOnly,
		JSON:       *jsonOut,
		OnTruncate: func(dropped int) {
			fmt.Fprintf(c.errOut, "beacon logs: предупреждение: пропущено %d более старых записей, показаны последние %d (--tail 0 показывает все)\n", dropped, *tail)
		},
	}
	lines, err := observe.ReadLogs(dir, filter)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon logs: %v\n", err)
		return 2
	}
	writeLines(c.out, lines)
	return 0
}

func writeLines(w io.Writer, lines []string) {
	for _, line := range lines {
		_, _ = fmt.Fprintln(w, line)
	}
}
