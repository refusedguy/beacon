package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// loadTimeout bounds API calls; visibleRows caps the number of displayed runs.
// refreshDebounce is the quiet window used to coalesce a burst of events into
// one runs read.
const (
	loadTimeout     = 5 * time.Second
	visibleRows     = 20
	refreshDebounce = 200 * time.Millisecond
)

// pingEvent is the daemon's keepalive event. It carries no run state, so it
// never schedules a refresh.
const pingEvent = "ping"

// blankPad supplies the spaces used to right-pad fixed-width row fields. Row
// padding never exceeds visibleRowWidth, so one literal serves every row.
const (
	blankPad        = "                                "
	visibleRowWidth = 24
)

// cursorStyle, headerStyle and errorStyle color the run list and status line.
var (
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
)

// renderCache holds scratch state that View reuses across frames. It sits
// behind a pointer so copies of model share the same buffer. The frame fields
// hold the last rendered string and the inputs that produced it, so an
// unchanged render returns the same string without rebuilding it.
type renderCache struct {
	padWidth int
	pad      string
	row      []byte
	keyRuns  []runFrame

	frame    string
	frameKey frameKey
	hasFrame bool
}

// runFrame is the part of a Run that View renders. It is the identity of one
// row in the full-frame cache key.
type runFrame struct {
	id, state, taskID, reason string
	pr                        int
}

// frameKey holds every input that can change View's output. A match means the
// cached frame is still current.
type frameKey struct {
	width  int
	cursor int
	status string
	failed bool
	runs   []runFrame
}

func newRunFrame(run Run) runFrame {
	return runFrame{
		id: run.ID, state: run.State, pr: run.PR,
		taskID: run.TaskID, reason: run.Reason,
	}
}

// frameCurrent reports whether the cached frame matches the current inputs.
// The runs are compared field by field on the values View actually uses.
func (c *renderCache) frameCurrent(width, cursor int, status string, failed bool, runs []Run) bool {
	if !c.hasFrame || c.frameKey.width != width || c.frameKey.cursor != cursor ||
		c.frameKey.status != status || c.frameKey.failed != failed {
		return false
	}
	if len(c.frameKey.runs) != len(runs) {
		return false
	}
	for i := range runs {
		if c.frameKey.runs[i] != newRunFrame(runs[i]) {
			return false
		}
	}
	return true
}

// rememberFrame stores a rendered frame together with the inputs that made it.
// The key slice is scratch space on the cache: after frameCurrent has compared
// the old key, the same backing array is refilled for the new one, so a rebuild
// does not allocate a fresh []runFrame on every miss.
func (c *renderCache) rememberFrame(width, cursor int, status string, failed bool, runs []Run, frame string) {
	keyRuns := c.keyRuns[:0]
	for i := range runs {
		keyRuns = append(keyRuns, newRunFrame(runs[i]))
	}
	c.keyRuns = keyRuns
	key := frameKey{
		width: width, cursor: cursor, status: status, failed: failed,
		runs: keyRuns,
	}
	c.frameKey = key
	c.frame = frame
	c.hasFrame = true
}

// model is the Bubble Tea state for the run monitor.
type model struct {
	client     *Client
	runs       []Run
	cursor     int
	status     string
	failed     bool
	width      int
	debounce   time.Duration
	refreshGen *atomic.Uint64
	render     *renderCache
}

// runsMsg carries a fetched run snapshot or its error. gen orders responses
// so a late reply from a superseded read cannot overwrite a newer snapshot.
type runsMsg struct {
	runs  []Run
	err   error
	gen   uint64
	stale bool
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
	return model{
		client:     c,
		status:     "loading runs from " + c.Endpoint,
		debounce:   refreshDebounce,
		refreshGen: &atomic.Uint64{},
		render:     &renderCache{},
	}
}

// Init starts the first run fetch.
func (m model) Init() tea.Cmd {
	return m.reload()
}

// reload returns a command that fetches the current run snapshot.
func (m model) reload() tea.Cmd {
	gen := m.refreshGen.Load()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		runs, err := m.client.Runs(ctx)
		return runsMsg{runs: runs, err: err, gen: gen}
	}
}

// onRefresh coalesces a burst of notifications into one runs read. A ping is
// dropped because it carries no state. Every other event advances the shared
// generation; after the debounce window only the newest generation performs a
// read, so no notification is lost and one GET serves the whole burst.
func (m model) onRefresh(ev Event) (tea.Model, tea.Cmd) {
	if ev.Type == pingEvent {
		return m, nil
	}
	gen := m.refreshGen.Add(1)
	genPtr := m.refreshGen
	wait := m.debounce
	return m, func() tea.Msg {
		time.Sleep(wait)
		if gen != genPtr.Load() {
			return runsMsg{gen: gen, stale: true}
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		runs, err := m.client.Runs(ctx)
		return runsMsg{runs: runs, err: err, gen: gen}
	}
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
		if msg.stale || msg.gen < m.refreshGen.Load() {
			return m, nil
		}
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
		case !msg.result.OK:
			m.failed = true
			m.status = string(msg.action) + " " + msg.runID + ": refused"
			if msg.result.Reason != "" {
				m.status += ": " + msg.result.Reason
			}
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

// View renders the run list and status line. The header padding and the row
// scratch buffer are reused across frames. A frame whose inputs have not
// changed returns the cached string; the rendered text is unchanged.
func (m model) View() string {
	cache := m.render
	if cache == nil {
		cache = &renderCache{}
	}
	if cache.frameCurrent(m.width, m.cursor, m.status, m.failed, m.runs) {
		return cache.frame
	}
	var b strings.Builder
	title := "Beacon runs"
	header := title + "\n"
	trailingNewline := false
	if m.width > 0 {
		if cache.padWidth != m.width {
			cache.padWidth = m.width
			cache.pad = strings.Repeat(" ", maxInt(0, m.width-len(title)))
		}
		header = title + cache.pad
		trailingNewline = true
	}
	b.WriteString(headerStyle.Render(header))
	if trailingNewline {
		b.WriteByte('\n')
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
	// Grow once for the header, the visible rows and the footer so the
	// builder does not reallocate while writing the frame.
	b.Grow(len(header) + 1 + 8 + (end-start)*(visibleRowWidth*2+64) + len(m.status) + 80)
	row := cache.row[:0]
	for i := start; i < end; i++ {
		run := m.runs[i]
		row = row[:0]
		writeField(&row, run.ID, visibleRowWidth)
		row = append(row, ' ')
		writeField(&row, run.State, 16)
		row = append(row, " pr="...)
		writeField(&row, strconv.Itoa(run.PR), 5)
		row = append(row, ' ')
		row = append(row, truncate(run.TaskID, visibleRowWidth)...)
		if run.Reason != "" {
			row = append(row, ' ')
			row = append(row, truncate(run.Reason, 32)...)
		}
		cache.row = row
		if i == m.cursor {
			b.WriteString(cursorStyle.Render("> " + string(row)))
		} else {
			b.WriteString("  ")
			b.Write(row)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	if m.failed {
		b.WriteString(errorStyle.Render(m.status))
	} else {
		b.WriteString(m.status)
	}
	b.WriteString("\n")
	b.WriteString(headerStyle.Render("j/k move  a approve  u unblock  c/x cancel  r retry  q quit"))
	frame := b.String()
	cache.rememberFrame(m.width, m.cursor, m.status, m.failed, m.runs, frame)
	return frame
}

// asciiOnly reports whether every byte of s is a single-byte UTF-8 code point.
func asciiOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// writeField appends s to dst and right-pads it with spaces to width runes.
// It matches the width verb of fmt.Sprintf("%-*s", width, s).
func writeField(dst *[]byte, s string, width int) {
	n := len(s)
	if !asciiOnly(s) {
		n = utf8.RuneCountInString(s)
	}
	*dst = append(*dst, s...)
	if n < width {
		pad := width - n
		if pad > len(blankPad) {
			pad = len(blankPad)
		}
		*dst = append(*dst, blankPad[:pad]...)
	}
}

// truncate shortens s to limit runes with an ellipsis. ASCII text skips the
// rune conversion because a byte is a rune and len(s) is the rune count.
func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if asciiOnly(s) {
		if len(s) <= limit {
			return s
		}
		return s[:limit-1] + "…"
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
