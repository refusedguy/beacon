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

// maxEventLines bounds the event tail kept in the model. The screen shows a
// short history, so an old frame never grows the model without limit.
const maxEventLines = 12

// CommandFunc handles one line of operator input. App passes a bound
// App.Command, which keeps one parser for the text and interactive front ends.
type CommandFunc func(line string) error

// SnapshotMsg carries a refreshed projection into the live board. Revision
// guards the panels: a refresh without a new revision leaves them unchanged.
type SnapshotMsg struct {
	Snap     Snapshot
	Revision uint64
	Err      error
}

// EventMsg carries one SSE frame into the event tail. A repeated ID is dropped
// so a replayed frame does not paint the same line twice.
type EventMsg struct {
	ID   string
	Line string
}

// CommandMsg carries the outcome of an operator command. An error means the
// server refused the command and is shown in the status line.
type CommandMsg struct {
	Line string
	Err  error
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

	// Live state. Zero values leave the static board untouched.
	live        bool
	snap        Snapshot
	revision    uint64
	lastErr     error
	events      []string
	lastEventID string
	statusLine  string
	watchCursor int
	command     CommandFunc
	input       string
	editing     bool
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

// NewLiveModel builds the one-screen board from a projection and the card
// index. The command handler runs when the operator finishes a command line.
func NewLiveModel(cards []Card, snap Snapshot, revision uint64, lastErr error, command CommandFunc) Model {
	m := NewModel(cards)
	m.live = true
	m.command = command
	m.applySnapshot(snap, revision, lastErr)
	return m
}

// applySnapshot advances the panels only for a newer revision. A failed read
// keeps the last projection but records the error for the header.
func (m *Model) applySnapshot(snap Snapshot, revision uint64, lastErr error) {
	if revision > m.revision {
		m.snap = snap
		m.revision = revision
		m.lastErr = lastErr
		if m.watchCursor >= len(snap.LiveWatches) {
			m.watchCursor = len(snap.LiveWatches) - 1
		}
		if m.watchCursor < 0 {
			m.watchCursor = 0
		}
		return
	}
	if lastErr != nil {
		m.lastErr = lastErr
	}
}

// pushEvent appends one frame to the tail and drops a repeated frame ID.
func (m *Model) pushEvent(id, line string) {
	if line == "" || (id != "" && id == m.lastEventID) {
		return
	}
	if id == "" && len(m.events) > 0 && m.events[len(m.events)-1] == line {
		return
	}
	if id != "" {
		m.lastEventID = id
	}
	m.events = append(m.events, line)
	if len(m.events) > maxEventLines {
		m.events = append([]string(nil), m.events[len(m.events)-maxEventLines:]...)
	}
}

// SelectedWatch returns the watch under the watch cursor, if any.
func (m Model) SelectedWatch() (Watch, bool) {
	if len(m.snap.LiveWatches) == 0 {
		return Watch{}, false
	}
	if m.watchCursor < 0 || m.watchCursor >= len(m.snap.LiveWatches) {
		return Watch{}, false
	}
	return m.snap.LiveWatches[m.watchCursor], true
}

// Status returns the last command result shown in the header.
func (m Model) Status() string { return m.statusLine }

// EventTail returns the bounded event tail in display order.
func (m Model) EventTail() []string { return append([]string(nil), m.events...) }

// Revision returns the revision of the snapshot on screen.
func (m Model) Revision() uint64 { return m.revision }

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

// Update moves the cursor, cycles filters, selects a watch and runs commands.
// Live messages only change the panels whose data advanced.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SnapshotMsg:
		m.applySnapshot(msg.Snap, msg.Revision, msg.Err)
		if msg.Err != nil {
			m.statusLine = "обновление: " + msg.Err.Error()
		}
		return m, nil
	case EventMsg:
		m.pushEvent(msg.ID, msg.Line)
		return m, nil
	case CommandMsg:
		if msg.Err != nil {
			m.statusLine = "отказ: " + msg.Err.Error()
		} else {
			m.statusLine = "выполнено: " + msg.Line
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.editing {
			return m.updateInput(msg)
		}
		if verb, ok := verbForKey(msg.String()); ok {
			return m.runVerb(verb)
		}
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
		case "left", "h":
			if m.watchCursor > 0 {
				m.watchCursor--
			}
		case "right", "l":
			if m.watchCursor < len(m.snap.LiveWatches)-1 {
				m.watchCursor++
			}
		case ":":
			m.editing = true
			m.input = ""
			return m, nil
		case "r":
			return m.runLine("r", "refresh")
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

// verbForKey finds the decide verb bound to a single key. The keys come from
// humanVerbs, so the screen and the parser share one verb list.
func verbForKey(key string) (string, bool) {
	for _, h := range humanVerbs {
		if h.key == key {
			return h.verb, true
		}
	}
	return "", false
}

// runVerb sends one decide verb to the selected watch. The WatchID comes from
// the visible cursor, so one keystroke replaces a typed command line.
func (m Model) runVerb(verb string) (tea.Model, tea.Cmd) {
	w, ok := m.SelectedWatch()
	if !ok {
		return m, func() tea.Msg {
			return CommandMsg{Line: verb, Err: fmt.Errorf("tui: no watch selected")}
		}
	}
	line := verb + " " + w.ID
	return m.runLine(line, line)
}

// runLine passes a prepared line to the bound CommandFunc. The result returns
// as a CommandMsg, which puts the receipt or the refusal in the status line.
func (m Model) runLine(line, label string) (tea.Model, tea.Cmd) {
	if m.command == nil {
		return m, func() tea.Msg {
			return CommandMsg{Line: label, Err: fmt.Errorf("tui: command handler not set")}
		}
	}
	run := m.command
	return m, func() tea.Msg {
		return CommandMsg{Line: label, Err: run(line)}
	}
}

// updateInput collects one command line and passes it to the bound CommandFunc
// on Enter. The refusal comes back as a CommandMsg and stays in the header.
func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		line := strings.TrimSpace(m.input)
		m.input = ""
		m.editing = false
		if line == "" {
			return m, nil
		}
		return m.runLine(line, line)
	case "backspace", "ctrl+h":
		runes := []rune(m.input)
		if len(runes) > 0 {
			m.input = string(runes[:len(runes)-1])
		}
	case "esc":
		m.editing = false
		m.input = ""
	default:
		for _, r := range msg.Runes {
			if r >= 32 && r != 127 {
				m.input += string(r)
			}
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

// View renders the live one-screen dashboard, or the static card board when
// no snapshot is attached. Both paths use the same model and Card contract.
func (m Model) View() string {
	if !m.live {
		return m.cardView()
	}
	var b strings.Builder
	b.WriteString(m.liveHeader())
	b.WriteString("\n")
	b.WriteString(m.cardView())
	b.WriteString("\n")
	b.WriteString(m.livePanels())
	return b.String()
}

// cardView renders the table, the detail pane, the graph and the key hints.
// The table is capped so the lower panes stay visible on a short terminal.
func (m Model) cardView() string {
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

func (m Model) liveHeader() string {
	s := m.snap
	points := fmt.Sprintf("%d/%d", s.PointStats.Used, s.PointStats.Limit)
	if s.PointStats.Limit == 0 {
		points = fmt.Sprintf("%d (бюджет %d)", s.PointStats.Used, s.PointStats.Budget)
	}
	header := fmt.Sprintf("beacon | points %s | watches %d | rev %d", points, len(s.LiveWatches), m.revision)
	if m.lastErr != nil {
		header += " | ошибка обновления: " + oneLine(m.lastErr.Error())
	}
	if m.statusLine != "" {
		header += " | " + oneLine(m.statusLine)
	}
	return headStyle.Render(header)
}

// helpVerbs renders the one-key shortcuts in short rows from humanVerbs. The
// help cannot promise a verb that the parser rejects.
func helpVerbs() []string {
	rows := []string{"", ""}
	for i, h := range humanVerbs {
		row := i / 5
		if rows[row] != "" {
			rows[row] += "  "
		}
		rows[row] += h.key + " " + h.verb
	}
	return rows
}

func (m Model) livePanels() string {
	var b strings.Builder
	b.WriteString(boxStyle.Render("ВОТЧИ\n" + m.watchLines()))
	b.WriteString("\n")
	b.WriteString(boxStyle.Render("БЛОКЕРЫ\n" + m.blockerLines()))
	b.WriteString("\n")
	b.WriteString(boxStyle.Render("ОЧЕРЕДЬ\n" + m.queueLines()))
	b.WriteString("\n")
	b.WriteString(boxStyle.Render("СОБЫТИЯ\n" + m.eventLines()))
	b.WriteString("\n")
	b.WriteString(boxStyle.Render("ПАМЯТЬ\n" + m.memoryLines()))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("j/k карточки   ←/→ вотчи   r обновить   : команда   q выход"))
	for _, row := range helpVerbs() {
		b.WriteString("\n")
		b.WriteString(helpStyle.Render(row))
	}
	if m.editing {
		b.WriteString("\n")
		b.WriteString(selStyle.Render("> " + m.input + "_"))
	}
	return b.String()
}

func (m Model) watchLines() string {
	watches := m.snap.LiveWatches
	if len(watches) == 0 {
		return "(нет вотчей)"
	}
	limit := m.listLimit(len(watches))
	var b strings.Builder
	for i := 0; i < limit; i++ {
		w := watches[i]
		blocked := ""
		if w.Blocked != nil && knownBlockReasons[w.Blocked.Reason] {
			blocked = " blocked=" + w.Blocked.Reason
		}
		mark := "  "
		if i == m.watchCursor {
			mark = "> "
		}
		fmt.Fprintf(&b, "%s#%d %s %s verdict=%s rev=%d%s\n", mark, w.PR, shortSHA(w.HeadSHA), w.Status, w.Verdict, w.Revision, blocked)
		for _, sub := range w.Subscribers {
			line := fmt.Sprintf("    %s фаза=%s", sub.ID, oneLine(sub.Phase))
			if role := oneLine(sub.Role); role != "" {
				line += " роль=" + role
			}
			if target := oneLine(sub.Target); target != "" {
				line += " target=" + target
			}
			b.WriteString(line + "\n")
		}
	}
	if b.Len() == 0 {
		return "(нет вотчей)"
	}
	if limit < len(watches) {
		fmt.Fprintf(&b, "… ещё %d", len(watches)-limit)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) blockerLines() string {
	var lines []string
	for _, inc := range m.snap.BlockerIncidents {
		lines = append(lines, fmt.Sprintf("%s %s: %s", inc.ID, inc.Kind, oneLine(inc.Reason)))
	}
	for _, esc := range m.snap.Escalations {
		lines = append(lines, "escalation "+esc)
	}
	for _, loadErr := range m.snap.LoadErrors {
		lines = append(lines, "load error "+loadErr)
	}
	if len(lines) == 0 {
		return "(нет)"
	}
	return strings.Join(lines[:m.listLimit(len(lines))], "\n")
}

func (m Model) queueLines() string {
	var lines []string
	for _, card := range m.cards {
		switch strings.ToLower(card.Status) {
		case "todo", "deferred":
			lines = append(lines, card.ID+" "+oneLine(card.Title))
		}
	}
	if len(lines) == 0 {
		return "(пусто)"
	}
	return strings.Join(lines[:m.listLimit(len(lines))], "\n")
}

func (m Model) eventLines() string {
	if len(m.events) == 0 {
		return "(пусто)"
	}
	limit := m.listLimit(len(m.events))
	return strings.Join(m.events[len(m.events)-limit:], "\n")
}

func (m Model) memoryLines() string {
	cycle := m.snap.LastCycle
	rss := "нет данных в снимке"
	if bytes := m.snap.Metrics.RSSBytes; bytes > 0 {
		rss = fmt.Sprintf("%.1f MiB (%d байт)", float64(bytes)/(1<<20), bytes)
	}
	return fmt.Sprintf("RSS: %s | цикл обновлено=%d за %d мс", rss, cycle.WatchesUpdated, cycle.DurationMs)
}

func (m Model) listLimit(total int) int {
	if total <= 0 {
		return 0
	}
	limit := total
	if m.height > 0 && limit > m.height/4 {
		limit = m.height / 4
	}
	if limit < 1 {
		limit = 1
	}
	return limit
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
