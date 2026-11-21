package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// boardRunner is the dispatch seam used by the other commands. The dispatch
// case for `beacon board` lives in cmd/beacon/main.go. RegisterBoard lets the
// wiring owner install the interactive implementation.
var boardRunner = func(c *CLI, args []string) int { return runBoard(c, args) }

// RegisterBoard installs the board command implementation.
func RegisterBoard(fn func(*CLI, []string) int) { boardRunner = fn }

// boardRow is the local view of one card. The interactive board lives in
// internal/tui; this file keeps the plain table runner used by tests and as
// the default before the adapter is installed.
type boardRow struct {
	id     string
	status string
	owner  string
	title  string
	phase  string
}

func runBoard(c *CLI, args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	root := fs.String("root", "", "корень проекта (по умолчанию каталог команды)")
	statusFilter := fs.String("status", "", "фильтр по статусу карточки")
	ownerFilter := fs.String("owner", "", "фильтр по владельцу карточки")
	if !parseFlags("board", args, c.errOut, fs) {
		return 2
	}

	projectRoot := *root
	if projectRoot == "" {
		projectRoot = c.root
	}
	cards, err := taskstore.LoadCards(filepath.Join(projectRoot, "docs", "tasks"))
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon board: %v\n", err)
		return 2
	}

	rows := make([]boardRow, 0, len(cards))
	for _, card := range cards {
		if *statusFilter != "" && !strings.EqualFold(card.Status, *statusFilter) {
			continue
		}
		if *ownerFilter != "" && !strings.EqualFold(card.Agent, *ownerFilter) {
			continue
		}
		rows = append(rows, boardRow{
			id:     card.ID,
			status: card.Status,
			owner:  card.Agent,
			title:  boardTitle(card),
			phase:  card.Phase,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })

	if err := renderBoardTable(c.out, rows); err != nil {
		fmt.Fprintf(c.errOut, "beacon board: %v\n", err)
		return 2
	}
	return 0
}

func boardTitle(card taskstore.Card) string {
	if card.Title != "" {
		return card.Title
	}
	return strings.Join(strings.Fields(card.Goal), " ")
}

func renderBoardTable(w io.Writer, rows []boardRow) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tOWNER\tPHASE\tTITLE")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", row.id, row.status, row.owner, row.phase, strings.Join(strings.Fields(row.title), " "))
	}
	return tw.Flush()
}
