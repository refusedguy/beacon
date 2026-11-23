package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
	"github.com/refusedguy/beacon/internal/tui"
)

// installBoardAdapter plugs the interactive board into the CLI dispatch. The
// adapter loads cards in cmd/beacon and hands them to internal/tui, which owns
// the Bubble Tea model and the plain-text fallback.
func installBoardAdapter() {
	RegisterBoard(func(c *CLI, args []string) int {
		return runTuiBoard(c, args, os.Stdin)
	})
}

func init() {
	installBoardAdapter()
}

// tuiRunner is the shared dispatch seam for the two missing live entry points.
// Both `beacon tui` and `beacon up --tui` call runLiveBoard, the adapter that
// `beacon board --live` already uses. All three routes reach App.RunLive and
// keep the plain-text snapshot for a pipe.
var tuiRunner = func(c *CLI, args []string) int { return runLiveBoard(c, args) }

// runTuiBoard reads the card index, applies the CLI filters and chooses the
// renderer. A terminal gets the interactive model; a pipe gets the table.
func runTuiBoard(c *CLI, args []string, stdin *os.File) int {
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
	stored, err := taskstore.LoadCards(filepath.Join(projectRoot, "docs", "tasks"))
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon board: %v\n", err)
		return 2
	}

	cards := make([]tui.Card, 0, len(stored))
	for _, card := range stored {
		if *statusFilter != "" && !strings.EqualFold(card.Status, *statusFilter) {
			continue
		}
		if *ownerFilter != "" && !strings.EqualFold(card.Agent, *ownerFilter) {
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

	if !boardHasTerminal(stdin) {
		if err := tui.RenderFallback(c.out, cards); err != nil {
			fmt.Fprintf(c.errOut, "beacon board: %v\n", err)
			return 2
		}
		return 0
	}
	if err := tui.Run(cards); err != nil {
		fmt.Fprintf(c.errOut, "beacon board: %v\n", err)
		return 2
	}
	return 0
}

// boardHasTerminal reports whether stdin is a real terminal. Pipes and
// /dev/null return false, so tests and CI get the fallback, not a Bubble Tea
// program that fails to open a raw terminal.
func boardHasTerminal(stdin *os.File) bool {
	if stdin == nil {
		return false
	}
	return term.IsTerminal(stdin.Fd())
}
