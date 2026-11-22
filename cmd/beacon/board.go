package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
	"github.com/refusedguy/beacon/internal/tui"
)

// boardRunner is the dispatch seam used by the other commands. The dispatch
// case for `beacon board` lives in cmd/beacon/main.go. RegisterBoard lets the
// wiring owner install the interactive implementation.
var boardRunner = func(c *CLI, args []string) int { return runBoard(c, args) }

// RegisterBoard installs the board command implementation. The live flag is
// handled here, so the installed adapter keeps owning the static path.
func RegisterBoard(fn func(*CLI, []string) int) {
	boardRunner = func(c *CLI, args []string) int {
		if boardLiveRequested(args) {
			return runLiveBoard(c, args)
		}
		return fn(c, args)
	}
}

// boardLiveRequested finds --live before the -- separator. A flag after the
// separator belongs to an external command and stays out of board.
func boardLiveRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--live" || strings.HasPrefix(arg, "--live=") {
			return true
		}
	}
	return false
}

// boardLivePort is the narrow seam used by beacon board --live. The transport
// lives in internal/tui; cmd only supplies the loaded cards and selects the
// renderer (ADR 0306, ADR 0314).
type boardLivePort interface {
	Refresh(context.Context) error
	RunLive(context.Context, []tui.Card) error
	View() string
}

var newBoardLivePort = func() (boardLivePort, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	conn, err := httpapi.LoadClientConn(dir)
	if err != nil {
		return nil, err
	}
	client, err := tui.NewClient("http://"+conn.Address(), conn.Token, nil)
	if err != nil {
		return nil, err
	}
	return tui.NewApp(client), nil
}

// runLiveBoard opens the one-screen live view for beacon board --live. Without
// a terminal it prints one rendered snapshot and exits.
func runLiveBoard(c *CLI, args []string) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	root := fs.String("root", "", "корень проекта (по умолчанию каталог команды)")
	statusFilter := fs.String("status", "", "фильтр по статусу карточки")
	ownerFilter := fs.String("owner", "", "фильтр по владельцу карточки")
	fs.Bool("live", false, "открыть живой экран")
	if !parseFlags("board", args, c.errOut, fs) {
		return 2
	}

	projectRoot := *root
	if projectRoot == "" {
		projectRoot = c.root
	}
	cards, err := loadLiveCards(projectRoot, *statusFilter, *ownerFilter)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon board --live: карточки: %v\n", err)
		return 2
	}
	port, err := newBoardLivePort()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon board --live: %v\n", err)
		return 2
	}

	ctx, cancel := watchLiveContext()
	defer cancel()
	if !boardHasTerminal(os.Stdin) {
		if err := port.Refresh(ctx); err != nil {
			fmt.Fprintf(c.errOut, "beacon board --live: снимок: %v\n", err)
			return 2
		}
		if _, err := fmt.Fprint(c.out, port.View()); err != nil {
			fmt.Fprintf(c.errOut, "beacon board --live: %v\n", err)
			return 2
		}
		return 0
	}
	if err := port.RunLive(ctx, cards); err != nil {
		fmt.Fprintf(c.errOut, "beacon board --live: %v\n", err)
		return 2
	}
	return 0
}

// loadLiveCards applies the board filters to the card index for the live view.
func loadLiveCards(projectRoot, statusFilter, ownerFilter string) ([]tui.Card, error) {
	stored, err := taskstore.LoadCards(filepath.Join(projectRoot, "docs", "tasks"))
	if err != nil {
		return nil, err
	}
	cards := make([]tui.Card, 0, len(stored))
	for _, card := range stored {
		if statusFilter != "" && !strings.EqualFold(card.Status, statusFilter) {
			continue
		}
		if ownerFilter != "" && !strings.EqualFold(card.Agent, ownerFilter) {
			continue
		}
		cards = append(cards, tui.Card{
			ID:           card.ID,
			Title:        boardTitle(card),
			Status:       card.Status,
			Phase:        card.Phase,
			Type:         card.Type,
			Owner:        card.Agent,
			Goal:         card.Goal,
			Context:      card.Context,
			Path:         card.Path,
			OwnerFiles:   card.OwnerFiles,
			Dependencies: card.Dependencies,
		})
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return cards, nil
}

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
