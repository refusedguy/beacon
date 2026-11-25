package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	loadTimeout = 5 * time.Second
	visibleRows = 20
)

var (
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
)

type model struct {
	client *Client
	runs   []Run
	cursor int
	status string
	failed bool
	width  int
}

type runsMsg struct {
	runs []Run
	err  error
}

type actionMsg struct {
	action Action
	runID  string
	result ControlResult
	err    error
}

type refreshMsg struct{}

func newModel(c *Client) model {
	return model{client: c, status: "loading runs from " + c.Endpoint}
}

func (m model) Init() tea.Cmd {
	return m.reload()
}

func (m model) reload() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		runs, err := m.client.Runs(ctx)
		return runsMsg{runs: runs, err: err}
	}
}

func (m model) selected() (Run, bool) {
	if m.cursor < 0 || m.cursor >= len(m.runs) {
		return Run{}, false
	}
	return m.runs[m.cursor], true
}

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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case refreshMsg:
		return m, m.reload()
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
