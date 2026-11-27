package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{"ascii unchanged", "abcdef", 10, "abcdef"},
		{"ascii clipped", "abcdef", 4, "abc…"},
		{"multibyte fits by rune count", "ééé", 4, "ééé"},
		{"cjk clipped", "日本語テスト", 4, "日本語…"},
		{"emoji clipped", "a😀b😀c", 4, "a😀b…"},
		{"cjk then ascii clipped", "界ab", 2, "界…"},
		{"non-positive limit", "界", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.input, tc.limit)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) produced invalid UTF-8: %q", tc.input, tc.limit, got)
			}
		})
	}
}

func TestStreamHTTPClientIsFresh(t *testing.T) {
	transport := &http.Transport{}
	base := &http.Client{Timeout: requestTimeout, Transport: transport}
	c := &Client{HTTP: base}

	streamClient := c.streamHTTPClient()
	if streamClient == base {
		t.Fatal("stream client must be a fresh *http.Client, not the base")
	}
	if streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout = %v, want 0", streamClient.Timeout)
	}
	if streamClient.Transport != transport {
		t.Fatal("stream client must reuse the configured transport")
	}
	if again := c.streamHTTPClient(); again == streamClient {
		t.Fatal("each call must return a fresh client")
	}
}

// testDebounce keeps the coalescing window short so tests resolve refresh
// commands quickly without changing the production default.
const testDebounce = time.Millisecond

// fastModel returns a model with a short debounce window.
func fastModel(c *Client) model {
	m := newModel(c)
	m.debounce = testDebounce
	return m
}

// TestEventRefreshesRenderedRuns drives the path main.go wires together. A
// real SSE event becomes a refreshMsg, the reload command fetches runs, and
// the resulting runsMsg renders the run in View.
func TestEventRefreshesRenderedRuns(t *testing.T) {
	const runID = "run-42"
	const taskID = "T-functional"
	runJSON := `{"runs":[{"id":"` + runID + `","task_id":"` + taskID + `","state":"ready","pr":42,"head_sha":"abc123","reason":"review approved","updated_at":"2026-11-25T12:00:00Z"}]}`
	eventJSON := `{"id":9,"type":"run.updated","run_id":"` + runID + `","at":"2026-11-25T12:00:00Z"}`

	runsHits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiEventsPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+eventJSON+"\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case apiRunsPath:
			select {
			case runsHits <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, runJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok")
	m := fastModel(client)

	events := make(chan Event, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- client.Stream(ctx, func(ev Event) {
			select {
			case events <- ev:
			default:
			}
		})
	}()

	var ev Event
	select {
	case ev = <-events:
	case <-ctx.Done():
		t.Fatal("no event received from /api/v1/events")
	}
	if ev.ID != 9 || ev.Type != "run.updated" || ev.RunID != runID {
		t.Fatalf("event: %+v", ev)
	}

	// main.go forwards the event in a refreshMsg; the event is a notification,
	// so Update must schedule a runs read regardless of its contents.
	next, cmd := m.Update(refreshMsg{event: ev})
	if cmd == nil {
		t.Fatal("Update(refreshMsg) returned no reload command")
	}
	raw := cmd()
	msg, ok := raw.(runsMsg)
	if !ok {
		t.Fatalf("reload command returned %T, want runsMsg", raw)
	}
	if msg.err != nil {
		t.Fatalf("reload: %v", msg.err)
	}
	select {
	case <-runsHits:
	default:
		t.Fatal("reload command did not request /api/v1/runs")
	}

	finalModel, followUp := next.Update(msg)
	if followUp != nil {
		t.Fatalf("runsMsg returned an unexpected command: %v", followUp)
	}
	final, ok := finalModel.(model)
	if !ok {
		t.Fatalf("Update(runsMsg) returned %T, want model", finalModel)
	}
	view := final.View()
	if !strings.Contains(view, runID) {
		t.Fatalf("View dropped run %q:\n%s", runID, view)
	}
	if !strings.Contains(view, taskID) {
		t.Fatalf("View dropped task %q:\n%s", taskID, view)
	}
	if !strings.Contains(view, "loaded 1 run(s)") {
		t.Fatalf("View status missing loaded count:\n%s", view)
	}

	cancel()
	if err := <-streamDone; err != nil {
		t.Fatalf("Stream: %v", err)
	}
	t.Logf("event id=%d type=%s run_id=%s", ev.ID, ev.Type, ev.RunID)
	t.Logf("reload command returned runsMsg with %d run(s)", len(msg.runs))
	t.Logf("rendered line: %q", strings.Split(view, "\n")[1])
}

// deltaFixture serves runs responses and counts every GET. The body can be
// replaced between reads so a test can prove an event refreshes the view.
type deltaFixture struct {
	server *httptest.Server
	hits   *int64
	body   atomic.Value
}

func newDeltaFixture(t *testing.T, runID string) *deltaFixture {
	t.Helper()
	var hits int64
	f := &deltaFixture{hits: &hits}
	f.setBody(runsBody(runID, "waiting_ci"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiRunsPath:
			atomic.AddInt64(&hits, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, f.body.Load().(string))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f.server = srv
	return f
}

func runsBody(runID, state string) string {
	return `{"runs":[{"id":"` + runID + `","task_id":"T-delta","state":"` + state + `","pr":42,"updated_at":"2026-11-26T10:00:00Z"}]}`
}

func (f *deltaFixture) setBody(body string) {
	f.body.Store(body)
}

func (f *deltaFixture) getCount() int64 {
	return atomic.LoadInt64(f.hits)
}

// loadedModel builds a model that has completed one initial runs read. The
// returned view is the baseline a refresh must replace with fresh state.
func loadedModel(t *testing.T, c *Client) (model, string) {
	t.Helper()
	m := fastModel(c)
	raw := m.reload()()
	msg, ok := raw.(runsMsg)
	if !ok || msg.err != nil {
		t.Fatalf("initial reload = %T %v, want runsMsg with no error", raw, msg.err)
	}
	next, cmd := m.Update(msg)
	if cmd != nil {
		t.Fatalf("runsMsg returned an unexpected command: %v", cmd)
	}
	loaded, ok := next.(model)
	if !ok {
		t.Fatalf("Update(runsMsg) returned %T, want model", next)
	}
	return loaded, loaded.View()
}

// TestKnownEventTriggersRunsRead proves an event for a known run schedules a
// runs read and that the fresh snapshot replaces the rendered row. The event
// is a notification; the run state comes from the API, not the event payload.
func TestKnownEventTriggersRunsRead(t *testing.T) {
	const runID = "run-delta"
	f := newDeltaFixture(t, runID)
	m, before := loadedModel(t, NewClient(f.server.URL, "tok"))
	if got := f.getCount(); got != 1 {
		t.Fatalf("runs GETs after load = %d, want 1", got)
	}
	if !strings.Contains(before, "waiting_ci") {
		t.Fatalf("baseline view missing waiting_ci:\n%s", before)
	}

	// The daemon advances the run. The notification arrives carrying run_id.
	f.setBody(runsBody(runID, "ready"))
	ev := Event{ID: 9, Type: "run.updated", RunID: runID, At: "2026-11-26T11:00:00Z"}

	next, cmd := m.Update(refreshMsg{event: ev})
	if cmd == nil {
		t.Fatal("a known-run event must schedule a runs read")
	}
	raw := cmd()
	msg, ok := raw.(runsMsg)
	if !ok || msg.err != nil || msg.stale {
		t.Fatalf("reload command = %T %v stale=%v, want fresh runsMsg", raw, msg.err, msg.stale)
	}
	if got := f.getCount(); got != 2 {
		t.Fatalf("known-run event caused %d runs GETs, want 2", got)
	}

	updated, followUp := next.Update(msg)
	if followUp != nil {
		t.Fatalf("runsMsg returned an unexpected command: %v", followUp)
	}
	final, ok := updated.(model)
	if !ok {
		t.Fatalf("Update(runsMsg) returned %T, want model", updated)
	}
	view := final.View()
	if strings.Contains(view, "waiting_ci") {
		t.Fatalf("view still shows stale state:\n%s", view)
	}
	if !strings.Contains(view, "ready") {
		t.Fatalf("view missing refreshed state:\n%s", view)
	}
}

// TestUnknownEventFallsBackToReload proves an event that names no loaded run
// still triggers a full runs read.
func TestUnknownEventFallsBackToReload(t *testing.T) {
	f := newDeltaFixture(t, "run-known")
	m, _ := loadedModel(t, NewClient(f.server.URL, "tok"))

	want := int64(2)
	for _, ev := range []Event{
		{ID: 2, Type: "pr_changed", RunID: "run-not-loaded"},
		{ID: 3, Type: "run.updated", RunID: "run-also-not-loaded"},
	} {
		_, cmd := m.Update(refreshMsg{event: ev})
		if cmd == nil {
			t.Fatalf("event %+v must fall back to a reload", ev)
		}
		raw := cmd()
		msg, ok := raw.(runsMsg)
		if !ok || msg.err != nil || msg.stale {
			t.Fatalf("fallback command = %T %v stale=%v, want fresh runsMsg", raw, msg.err, msg.stale)
		}
		if got := f.getCount(); got != want {
			t.Fatalf("fallback did not request /api/v1/runs: GETs = %d, want %d", got, want)
		}
		want++
	}
}

// TestPingDoesNotScheduleRefresh proves the daemon's keepalive is dropped
// before a refresh is scheduled. A ping carries no run state, so it neither
// advances the generation nor issues a read.
func TestPingDoesNotScheduleRefresh(t *testing.T) {
	f := newDeltaFixture(t, "run-ping")
	m, _ := loadedModel(t, NewClient(f.server.URL, "tok"))
	base := f.getCount()
	gen := m.refreshGen.Load()

	for _, msg := range []tea.Msg{
		refreshMsg{event: Event{ID: 1, Type: "ping"}},
		Event{ID: 2, Type: "ping"},
	} {
		next, cmd := m.Update(msg)
		if cmd != nil {
			t.Fatalf("ping must not schedule a refresh, got %v", cmd)
		}
		if got := next.(model).refreshGen.Load(); got != gen {
			t.Fatalf("ping advanced generation to %d, want %d", got, gen)
		}
	}
	if got := f.getCount(); got != base {
		t.Fatalf("pings caused %d runs GETs, want %d", got, base)
	}
}

// TestBurstDebouncedToSingleReload is the task check: five events arriving in
// a burst issue one runs read and the view shows the newest snapshot.
func TestBurstDebouncedToSingleReload(t *testing.T) {
	const runID = "run-burst"
	f := newDeltaFixture(t, runID)
	m, _ := loadedModel(t, NewClient(f.server.URL, "tok"))
	base := f.getCount()

	var cmds []tea.Cmd
	for i := 0; i < 5; i++ {
		f.setBody(runsBody(runID, fmt.Sprintf("state-%d", i)))
		ev := Event{ID: int64(i + 1), Type: "pr_changed", RunID: runID}
		_, cmd := m.Update(refreshMsg{event: ev})
		if cmd == nil {
			t.Fatalf("event %d did not schedule a refresh", i)
		}
		cmds = append(cmds, cmd)
	}

	var latest runsMsg
	reads := 0
	for i, cmd := range cmds {
		raw := cmd()
		msg, ok := raw.(runsMsg)
		if !ok {
			t.Fatalf("refresh %d returned %T, want runsMsg", i, raw)
		}
		if msg.err != nil {
			t.Fatalf("refresh %d: %v", i, msg.err)
		}
		if !msg.stale {
			reads++
			latest = msg
		}
	}
	if reads != 1 {
		t.Fatalf("burst issued %d runs reads, want 1", reads)
	}
	if got := f.getCount(); got != base+1 {
		t.Fatalf("burst caused %d total runs GETs, want %d", got, base+1)
	}

	final, followUp := m.Update(latest)
	if followUp != nil {
		t.Fatalf("runsMsg returned an unexpected command: %v", followUp)
	}
	updated, ok := final.(model)
	if !ok {
		t.Fatalf("Update(runsMsg) returned %T, want model", final)
	}
	view := updated.View()
	if !strings.Contains(view, "state-4") {
		t.Fatalf("view does not show newest snapshot state-4:\n%s", view)
	}
	for i := 0; i < 4; i++ {
		if strings.Contains(view, fmt.Sprintf("state-%d", i)) {
			t.Fatalf("view shows superseded state-%d:\n%s", i, view)
		}
	}
}

// TestNewerSnapshotWins proves a late reply from a superseded read cannot
// overwrite a newer snapshot in the view.
func TestNewerSnapshotWins(t *testing.T) {
	const runID = "run-order"
	f := newDeltaFixture(t, runID)
	m, _ := loadedModel(t, NewClient(f.server.URL, "tok"))

	f.setBody(runsBody(runID, "older-state"))
	_, firstCmd := m.Update(refreshMsg{event: Event{ID: 1, Type: "run.updated", RunID: runID}})
	firstRaw := firstCmd()
	older, ok := firstRaw.(runsMsg)
	if !ok || older.err != nil || older.stale {
		t.Fatalf("first refresh = %T %v stale=%v, want fresh runsMsg", firstRaw, older.err, older.stale)
	}

	f.setBody(runsBody(runID, "newer-state"))
	_, secondCmd := m.Update(refreshMsg{event: Event{ID: 2, Type: "run.updated", RunID: runID}})
	secondRaw := secondCmd()
	newer, ok := secondRaw.(runsMsg)
	if !ok || newer.err != nil || newer.stale {
		t.Fatalf("second refresh = %T %v stale=%v, want fresh runsMsg", secondRaw, newer.err, newer.stale)
	}

	afterNewer, _ := m.Update(newer)
	want := afterNewer.(model).View()
	if !strings.Contains(want, "newer-state") {
		t.Fatalf("view missing newer snapshot:\n%s", want)
	}

	afterOlder, followUp := afterNewer.Update(older)
	if followUp != nil {
		t.Fatalf("stale runsMsg returned an unexpected command: %v", followUp)
	}
	got := afterOlder.(model).View()
	if got != want {
		t.Fatalf("late response overwrote newer snapshot:\n%s\nwant:\n%s", got, want)
	}
}

// controlFixture serves a run list and one control response, counting every
// control POST. It drives apply() from a selected run to the rendered status.
// A non-zero status makes the control response a refusal on the wire.
type controlFixture struct {
	server  *httptest.Server
	control string
	status  int
	posts   *int64
}

func newControlFixture(t *testing.T, runID, controlBody string) *controlFixture {
	t.Helper()
	var posts int64
	f := &controlFixture{control: controlBody, posts: &posts}
	runsJSON := `{"runs":[{"id":"` + runID + `","task_id":"T-control","state":"closed","pr":42,"head_sha":"abc123","updated_at":"2026-11-26T10:00:00Z"}]}`
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiRunsPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, runsJSON)
		case apiControlPath:
			atomic.AddInt64(&posts, 1)
			w.Header().Set("Content-Type", "application/json")
			if f.status != 0 {
				w.WriteHeader(f.status)
			}
			_, _ = io.WriteString(w, f.control)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *controlFixture) loaded(t *testing.T) model {
	t.Helper()
	m, _ := loadedModel(t, NewClient(f.server.URL, "tok"))
	return m
}

// TestRefusalRendersReason is the task check. A control refusal arrives as a
// 409 with the daemon's reason on the wire, and the panel shows that reason.
func TestRefusalRendersReason(t *testing.T) {
	const reason = "run is closed"
	f := newControlFixture(t, "run-control", `{"ok":false,"action":"approve","run_id":"run-control","applied":false,"reason":"`+reason+`"}`)
	f.status = http.StatusConflict
	m := f.loaded(t)

	raw := m.apply(ActionApprove)()
	msg, ok := raw.(actionMsg)
	if !ok {
		t.Fatalf("apply returned %T, want actionMsg", raw)
	}
	if msg.err != nil {
		t.Fatalf("apply: %v", msg.err)
	}
	if msg.result.OK || msg.result.Reason != reason {
		t.Fatalf("refusal result = %+v, want OK false and reason %q", msg.result, reason)
	}
	next, followUp := m.Update(msg)
	if followUp != nil {
		t.Fatalf("refusal returned an unexpected command: %v", followUp)
	}
	view := next.(model).View()
	if !strings.Contains(view, "refused") {
		t.Fatalf("view missing refusal status:\n%s", view)
	}
	if !strings.Contains(view, reason) {
		t.Fatalf("view missing daemon reason %q:\n%s", reason, view)
	}
	if got := atomic.LoadInt64(f.posts); got != 1 {
		t.Fatalf("control POSTs = %d, want 1", got)
	}
}

// TestApprovedKeepsDisplay proves an approved action keeps the existing status
// and still schedules a reload.
func TestApprovedKeepsDisplay(t *testing.T) {
	f := newControlFixture(t, "run-control", `{"ok":true,"action":"approve","run_id":"run-control","applied":true}`)
	m := f.loaded(t)

	msg := m.apply(ActionApprove)().(actionMsg)
	next, followUp := m.Update(msg)
	if followUp == nil {
		t.Fatal("approved action must schedule a reload")
	}
	view := next.(model).View()
	if !strings.Contains(view, "approve run-control: applied") {
		t.Fatalf("approved display changed:\n%s", view)
	}
	if strings.Contains(view, "refused") {
		t.Fatalf("approved action reported a refusal:\n%s", view)
	}
}

// TestMissingAppliedUsesOK proves ok alone decides an accepted response. An
// omitted applied field does not turn it into a refusal.
func TestMissingAppliedUsesOK(t *testing.T) {
	f := newControlFixture(t, "run-control", `{"ok":true,"action":"approve","run_id":"run-control"}`)
	m := f.loaded(t)

	msg := m.apply(ActionApprove)().(actionMsg)
	next, followUp := m.Update(msg)
	if followUp == nil {
		t.Fatal("ok true with no applied must be treated as applied")
	}
	view := next.(model).View()
	if !strings.Contains(view, "approve run-control: applied") || strings.Contains(view, "refused") {
		t.Fatalf("view for missing applied:\n%s", view)
	}
}

// truncateRune is the pre-optimization truncator. It stays in the test so the
// baseline renderer produces the same text the optimized View must produce.
func truncateRune(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}

// viewBaseline is the pre-optimization renderer. It stays in the test so the
// identity test can pin the rendered bytes and the benchmark can report the
// allocations the ASCII fast path, cached padding and reused row buffer save.
func viewBaseline(m model) string {
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
		line := fmt.Sprintf("%-24s %-16s pr=%-5d %s", run.ID, run.State, run.PR, truncateRune(run.TaskID, 24))
		if run.Reason != "" {
			line += " " + truncateRune(run.Reason, 32)
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

// viewModel builds a model with a live render cache so repeated View calls
// exercise the reused padding and row buffer.
func viewModel(width, cursor int, runs []Run, status string, failed bool) model {
	m := newModel(&Client{Endpoint: "http://127.0.0.1:7432"})
	m.width = width
	m.cursor = cursor
	m.runs = runs
	m.status = status
	m.failed = failed
	return m
}

// benchmarkFiveRuns builds a deterministic five-run snapshot for the view
// identity test and the alternating benchmark.
func benchmarkFiveRuns(state string) []Run {
	states := []string{state, "ready", "waiting_ci", "closed", "open"}
	runs := make([]Run, 5)
	for i := range runs {
		runs[i] = Run{
			ID:     fmt.Sprintf("run-%04d", i),
			TaskID: fmt.Sprintf("T-%04d", i),
			State:  states[i],
			PR:     i + 1,
			Reason: "review approved",
		}
	}
	return runs
}

// TestViewMatchesBaseline is the byte-identity check. The optimized View must
// match the old renderer on every frame, including after its cache warms up.
func TestViewMatchesBaseline(t *testing.T) {
	runs := benchmarkFiveRuns("ready")
	wide := benchmarkFiveRuns("ready")
	wide[0].TaskID = "日本語テストタスク"
	wide[0].Reason = "レビュー待ちの理由がとても長い"
	wide[1].TaskID = "a😀b😀c😀d"
	wide[1].Reason = "ready to merge"

	cases := []struct {
		name  string
		model model
	}{
		{"empty no width", viewModel(0, 0, nil, "loading runs", false)},
		{"empty with width", viewModel(80, 0, nil, "loaded 0 run(s)", false)},
		{"five runs", viewModel(80, 0, runs, "loaded 5 run(s)", false)},
		{"cursor last", viewModel(80, 4, runs, "loaded 5 run(s)", false)},
		{"failed status", viewModel(60, 2, runs, "runs: boom", true)},
		{"narrow width", viewModel(5, 1, runs, "loaded 5 run(s)", false)},
		{"multibyte fields", viewModel(80, 0, wide, "loaded 5 run(s)", false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := viewBaseline(tc.model)
			for frame := 0; frame < 3; frame++ {
				if got := tc.model.View(); got != want {
					t.Fatalf("frame %d: View differs from baseline:\ngot:\n%s\nwant:\n%s", frame, got, want)
				}
			}
		})
	}
}

var benchViewOut string

// BenchmarkViewFiveRuns alternates two five-run snapshots. Both sides render
// the same five rows and report identical text; the reused side shows the lower
// allocation count.
func BenchmarkViewFiveRuns(b *testing.B) {
	fixtures := [2]model{
		viewModel(80, 0, benchmarkFiveRuns("waiting_ci"), "loaded 5 run(s)", false),
		viewModel(80, 3, benchmarkFiveRuns("ready"), "loaded 5 run(s)", false),
	}
	want := [2]string{viewBaseline(fixtures[0]), viewBaseline(fixtures[1])}
	for i := range fixtures {
		if got := fixtures[i].View(); got != want[i] {
			b.Fatalf("fixture %d view differs from baseline:\ngot:\n%s\nwant:\n%s", i, got, want[i])
		}
	}
	b.Run("baseline", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(5, "rows/op")
		for i := 0; i < b.N; i++ {
			benchViewOut = viewBaseline(fixtures[i&1])
		}
	})
	b.Run("reused", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(5, "rows/op")
		for i := 0; i < b.N; i++ {
			benchViewOut = fixtures[i&1].View()
		}
	})
}

// TestViewCacheFollowsWidthChanges pins header invalidation. The cached styled
// header must be rebuilt when the terminal width changes, including a switch
// back to zero width.
func TestViewCacheFollowsWidthChanges(t *testing.T) {
	m := viewModel(80, 1, benchmarkFiveRuns("ready"), "loaded 5 run(s)", false)
	for _, width := range []int{80, 0, 41, 80, 5, 0} {
		m.width = width
		want := viewBaseline(m)
		for frame := 0; frame < 2; frame++ {
			if got := m.View(); got != want {
				t.Fatalf("width %d frame %d: View differs from baseline:\ngot:\n%s\nwant:\n%s", width, frame, got, want)
			}
		}
	}
}

// perFrameCache is the scratch state View held before the header, legend and
// size-hint cache. Keeping it in the test lets the benchmark isolate that cut.
type perFrameCache struct {
	padWidth int
	pad      string
	row      []byte
}

// viewPerFrame mirrors View before the cache cut. It still reuses the row
// buffer and the header padding, so only the repeated style renders and the
// builder growth differ from the current View.
func viewPerFrame(m model, cache *perFrameCache) string {
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
	b.WriteString(headerStyle.Render(helpText))
	return b.String()
}

var benchCacheOut string

// BenchmarkViewCache alternates two five-row snapshots on each side. Both
// sides render five rows and identical bytes; the rows/op metric makes a
// shortcut that skipped rows visible in the report.
func BenchmarkViewCache(b *testing.B) {
	fixtures := [2]model{
		viewModel(80, 0, benchmarkFiveRuns("waiting_ci"), "loaded 5 run(s)", false),
		viewModel(80, 3, benchmarkFiveRuns("ready"), "loaded 5 run(s)", false),
	}
	caches := [2]perFrameCache{{}, {}}
	for i := range fixtures {
		want := viewBaseline(fixtures[i])
		if got := viewPerFrame(fixtures[i], &caches[i]); got != want {
			b.Fatalf("fixture %d per-frame view differs from baseline", i)
		}
		if got := fixtures[i].View(); got != want {
			b.Fatalf("fixture %d cached view differs from baseline", i)
		}
	}
	b.Run("per-frame", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(5, "rows/op")
		for i := 0; i < b.N; i++ {
			benchCacheOut = viewPerFrame(fixtures[i&1], &caches[i&1])
		}
	})
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(5, "rows/op")
		for i := 0; i < b.N; i++ {
			benchCacheOut = fixtures[i&1].View()
		}
	})
}
