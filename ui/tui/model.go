package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// loadTimeout bounds API calls; visibleRows caps the number of displayed runs.
const (
	loadTimeout = 5 * time.Second
	visibleRows = 20
)

// cursorStyle, headerStyle and errorStyle color the run list and status line.
var (
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
)

// model is the Bubble Tea state for the run monitor.
type model struct {
	client *Client
	runs   []Run
	cursor int
	status string
	failed bool
	width  int
}

// runsMsg carries a fetched run snapshot or its error.
type runsMsg struct {
	runs []Run
	err  error
}

// actionMsg carries the result of a control action.
type actionMsg struct {
	action Action
	runID  string
	result ControlResult
	err    error
}

// refreshMsg requests a refresh after an event. The event is a notification:
// run state always comes from a fresh runs read, not from the event payload.
type refreshMsg struct {
	event Event
}

// newModel builds the initial monitor model for c.
func newModel(c *Client) model {
	return model{client: c, status: "loading runs from " + c.Endpoint}
}

// Init starts the first run fetch.
func (m model) Init() tea.Cmd {
	return m.reload()
}

// reload returns a command that fetches the current run snapshot.
func (m model) reload() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		runs, err := m.client.Runs(ctx)
		return runsMsg{runs: runs, err: err}
	}
}

// onRefresh treats every event as a notification and schedules a runs read.
// The event payload cannot carry run state, so a known run is refreshed from
// the API like any other run.
func (m model) onRefresh(_ Event) (tea.Model, tea.Cmd) {
	return m, m.reload()
}

// selected returns the run under the cursor.
func (m model) selected() (Run, bool) {
	if m.cursor < 0 || m.cursor >= len(m.runs) {
		return Run{}, false
	}
	return m.runs[m.cursor], true
}

// apply returns a command that sends action for the selected run.
func (m model) apply(action Action) tea.Cmd {
	run, ok := m.selected()
	if !ok {
		return func() tea.Msg {
			return actionMsg{action: action, err: fmt.Errorf("no run selected")}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		result, err := m.client.Control(ctx, ControlRequest{
			Action: action,
			RunID:  run.ID,
			SHA:    run.HeadSHA,
		})
		return actionMsg{action: action, runID: run.ID, result: result, err: err}
	}
}

// Update applies a Bubble Tea message to the model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case refreshMsg:
		return m.onRefresh(msg.event)
	case Event:
		return m.onRefresh(msg)
	case runsMsg:
		if msg.err != nil {
			m.failed = true
			m.status = "runs: " + msg.err.Error()
			return m, nil
		}
		m.failed = false
		m.runs = msg.runs
		if m.cursor >= len(m.runs) {
			m.cursor = len(m.runs) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.status = fmt.Sprintf("loaded %d run(s)", len(m.runs))
		return m, nil
	case actionMsg:
		m.failed = msg.err != nil
		switch {
		case msg.err != nil:
			m.status = string(msg.action) + " " + msg.runID + ": " + msg.err.Error()
		case !msg.result.OK || !msg.result.Applied:
			m.failed = true
			m.status = string(msg.action) + " " + msg.runID + ": refused"
		default:
			m.status = string(msg.action) + " " + msg.runID + ": applied"
			return m, m.reload()
		}
		return m, nil
	}
	return m, nil
}

// onKey handles a key press and returns the updated model.
func (m model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.runs)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "a":
		return m, m.apply(ActionApprove)
	case "u":
		return m, m.apply(ActionUnblock)
	case "c", "x":
		return m, m.apply(ActionCancel)
	case "r":
		return m, m.apply(ActionRetry)
	}
	return m, nil
}

// View renders the run list and status line.
func (m model) View() string {
	var b strings.Builder
	title := "Beacon runs"
	if m.width > 0 {
		b.WriteString(headerStyle.Render(title + strings.Repeat(" ", maxInt(0, m.width-len(title)))))
		b.WriteString("\n")
	} else {
		b.WriteString(headerStyle.Render(title + "\n"))
	}
	if len(m.runs) == 0 {
		b.WriteString("no runs\n")
	}
	start := 0
	if m.cursor >= visibleRows {
		start = m.cursor - visibleRows + 1
	}
	end := start + visibleRows
	if end > len(m.runs) {
		end = len(m.runs)
	}
	for i := start; i < end; i++ {
		run := m.runs[i]
		line := fmt.Sprintf("%-24s %-16s pr=%-5d %s", run.ID, run.State, run.PR, truncate(run.TaskID, 24))
		if run.Reason != "" {
			line += " " + truncate(run.Reason, 32)
		}
		if i == m.cursor {
			line = cursorStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	if m.failed {
		b.WriteString(errorStyle.Render(m.status))
	} else {
		b.WriteString(m.status)
	}
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("j/k move  a approve  u unblock  c/x cancel  r retry  q quit"))
	return b.String()
}

// truncate shortens s to limit runes with an ellipsis.
func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}

// maxInt returns the larger of a and b.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
