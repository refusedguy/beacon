// Package tui holds the local Beacon board. The board reads cards that a
// caller has already loaded, renders them with Bubble Tea and Lip Gloss, and
// keeps a tabwriter printer for terminals that cannot run an interactive
// program. Nothing here opens a socket, a database or a network connection.
package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Card is one row of the board. The caller fills these from the card index,
// so this package never needs to know where the index comes from.
type Card struct {
	ID           string
	Title        string
	Status       string
	Phase        string
	Type         string
	Owner        string
	Goal         string
	Context      string
	Path         string
	OwnerFiles   []string
	Dependencies []string
}

// Filters narrows the board by card status and by the owner recorded in the
// card. An empty string means no restriction on that field.
type Filters struct {
	Status string
	Owner  string
}

// depGraph is a small dependency graph built from the card index. It keeps
// only edges between cards that are present in the index, and traversal uses
// a visited set so a malformed index cannot produce an endless walk.
type depGraph struct {
	edges map[string][]string
}

func buildDepGraph(cards []Card) *depGraph {
	known := make(map[string]bool, len(cards))
	for _, card := range cards {
		known[card.ID] = true
	}
	g := &depGraph{edges: make(map[string][]string)}
	for _, card := range cards {
		for _, dep := range card.Dependencies {
			if dep != "" && dep != card.ID && known[dep] {
				g.edges[card.ID] = append(g.edges[card.ID], dep)
			}
		}
	}
	for from := range g.edges {
		sort.Strings(g.edges[from])
	}
	return g
}

// Lines lists the dependencies reachable from start as ASCII edges. Each line
// names the source and the target, and indentation shows the distance from the
// start card. A target is expanded once even when several paths reach it.
func (g *depGraph) Lines(start string) []string {
	type node struct {
		id    string
		depth int
	}
	seen := map[string]bool{start: true}
	queue := []node{{id: start}}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		deps := g.edges[cur.id]
		if len(deps) == 0 {
			continue
		}
		for _, dep := range deps {
			prefix := strings.Repeat("  ", cur.depth)
			out = append(out, prefix+cur.id+" -> "+dep)
			if !seen[dep] {
				seen[dep] = true
				queue = append(queue, node{id: dep, depth: cur.depth + 1})
			}
		}
	}
	if len(out) == 0 {
		return []string{"(нет рёбер в индексе)"}
	}
	return out
}

// Model is one board screen. It holds the full card list, the filtered view,
// the current cursor and the active filters. Rendering reads only these
// fields, so the same model can be driven by key presses or by a test.
type Model struct {
	cards     []Card
	filtered  []Card
	graph     *depGraph
	cursor    int
	statusIdx int
	ownerIdx  int
	statuses  []string
	owners    []string
	showGraph bool
	quitting  bool
	width     int
	height    int
}

// NewModel builds a board from a card list. The filter options are sorted
// unique values from the cards, with index zero reserved for "all".
func NewModel(cards []Card) Model {
	m := Model{
		cards:    append([]Card(nil), cards...),
		graph:    buildDepGraph(cards),
		statuses: uniqueField(cards, func(c Card) string { return c.Status }),
		owners:   uniqueField(cards, func(c Card) string { return c.Owner }),
	}
	m.applyFilters()
	return m
}

func uniqueField(cards []Card, field func(Card) string) []string {
	set := map[string]bool{}
	for _, card := range cards {
		if value := field(card); value != "" {
			set[value] = true
		}
	}
	out := make([]string, 0, len(set)+1)
	out = append(out, "")
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out[1:])
	return out
}

func (m *Model) applyFilters() {
	m.filtered = m.filtered[:0]
	status := m.statusFilter()
	owner := m.ownerFilter()
	for _, card := range m.cards {
		if status != "" && card.Status != status {
			continue
		}
		if owner != "" && card.Owner != owner {
			continue
		}
		m.filtered = append(m.filtered, card)
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) statusFilter() string {
	if m.statusIdx <= 0 || m.statusIdx >= len(m.statuses) {
		return ""
	}
	return m.statuses[m.statusIdx]
}

func (m Model) ownerFilter() string {
	if m.ownerIdx <= 0 || m.ownerIdx >= len(m.owners) {
		return ""
	}
	return m.owners[m.ownerIdx]
}

// Filters returns the filters currently applied to the board.
func (m Model) Filters() Filters {
	return Filters{Status: m.statusFilter(), Owner: m.ownerFilter()}
}

// Selected returns the card under the cursor, if any.
func (m Model) Selected() (Card, bool) {
	if len(m.filtered) == 0 {
		return Card{}, false
	}
	return m.filtered[m.cursor], true
}

// Init satisfies tea.Model. The board has no background work.
func (m Model) Init() tea.Cmd { return nil }

// Update moves the cursor, cycles filters and toggles the graph. Every other
// message is ignored so window resizes do not lose the current selection.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
		case "s":
			m.statusIdx++
			if m.statusIdx >= len(m.statuses) {
				m.statusIdx = 0
			}
			m.applyFilters()
		case "o":
			m.ownerIdx++
			if m.ownerIdx >= len(m.owners) {
				m.ownerIdx = 0
			}
			m.applyFilters()
		case "g":
			m.showGraph = !m.showGraph
		case "esc":
			m.statusIdx = 0
			m.ownerIdx = 0
			m.applyFilters()
		}
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Padding(0, 1)
	metaStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	headStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("117"))
	rowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	selStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")).Background(lipgloss.Color("238"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	helpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 1)
)

// View renders the table, the detail pane, the graph and the key hints. The
// table is capped so the lower panes stay visible on a short terminal.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("beacon board"))
	b.WriteString("\n")
	b.WriteString(metaStyle.Render(fmt.Sprintf(
		"карточек %d / %d   статус=%s   владелец=%s",
		len(m.filtered), len(m.cards), filterLabel(m.statusFilter()), filterLabel(m.ownerFilter()),
	)))
	b.WriteString("\n\n")

	b.WriteString(headStyle.Render(pad("ID", 14) + pad("СТАТУС", 12) + pad("ВЛАДЕЛЕЦ", 14) + "ЗАГОЛОВОК"))
	b.WriteString("\n")
	limit := len(m.filtered)
	if m.height > 0 && limit > m.height/3 {
		limit = m.height / 3
	}
	if limit < 1 && len(m.filtered) > 0 {
		limit = 1
	}
	for i := 0; i < limit; i++ {
		card := m.filtered[i]
		line := pad(card.ID, 14) + pad(card.Status, 12) + pad(card.Owner, 14) + oneLine(card.Title)
		if i == m.cursor {
			line = selStyle.Render(line)
		} else {
			line = rowStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(m.filtered) == 0 {
		b.WriteString(metaStyle.Render("нет карточек под текущие фильтры"))
		b.WriteString("\n")
	}

	detail := "(карточка не выбрана)"
	var graph []string
	if card, ok := m.Selected(); ok {
		detail = fmt.Sprintf("%s  [%s / %s]\n%s\n\nВладельцы файлов: %s\nЗависимости: %s",
			card.Title, card.Status, card.Phase,
			oneLine(card.Goal),
			joinOrNone(card.OwnerFiles),
			joinOrNone(card.Dependencies))
		graph = m.graph.Lines(card.ID)
	} else {
		graph = []string{"(карточка не выбрана)"}
	}

	panes := []string{boxStyle.Render("ДЕТАЛИ\n" + detail)}
	if m.showGraph {
		panes = append(panes, boxStyle.Render("ГРАФ\n"+strings.Join(graph, "\n")))
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, panes...))
	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("j/k или ↑/↓ выбор   s статус   o владелец   g граф   esc сброс   q выход"))
	return b.String()
}

func filterLabel(value string) string {
	if value == "" {
		return "все"
	}
	return value
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "нет"
	}
	return strings.Join(values, ", ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func pad(s string, width int) string {
	runes := []rune(s)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return s + strings.Repeat(" ", width-len(runes))
}

// RenderFallback prints the board as a plain table. It is used when stdin or
// stdout is not a terminal, so the same card list stays available without an
// interactive program.
func RenderFallback(w io.Writer, cards []Card) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tOWNER\tTITLE")
	for _, card := range cards {
		title := oneLine(card.Title)
		if title == "" {
			title = oneLine(card.Goal)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", card.ID, card.Status, card.Owner, title)
	}
	return tw.Flush()
}

// Run starts the interactive board and blocks until the user quits. The
// caller decides whether the terminal is interactive before calling it.
func Run(cards []Card) error {
	program := tea.NewProgram(NewModel(cards), tea.WithAltScreen())
	_, err := program.Run()
	return err
}
