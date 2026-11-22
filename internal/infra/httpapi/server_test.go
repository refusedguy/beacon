package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

type stubProvider struct {
	snap Snapshot
	err  error
}

func (p stubProvider) Snapshot(context.Context) (Snapshot, error) { return p.snap, p.err }

type stubSink struct {
	calls  []Decision
	result DecisionResult
	err    error
}

func (s *stubSink) Apply(_ context.Context, d Decision) (DecisionResult, error) {
	s.calls = append(s.calls, d)
	if s.err != nil {
		return DecisionResult{}, s.err
	}
	return s.result, nil
}

func newTestServer(t *testing.T, provider StateProvider, sink DecisionSink, bus *EventBus) *Server {
	t.Helper()
	cfg := Config{
		StateDir:      t.TempDir(),
		Version:       "v-test",
		ProjectKey:    "project-key",
		IsPollerOwner: true,
		StartedAt:     time.Now().Add(-3 * time.Second),
		Provider:      provider,
		Sink:          sink,
		Events:        bus,
	}
	return newServer(cfg, "tok", "127.0.0.1", 7432, bus)
}

func authorized(r *http.Request) *http.Request {
	r.Header.Set("Authorization", "Bearer tok")
	return r
}

func TestHealth(t *testing.T) {
	provider := stubProvider{snap: Snapshot{LoadErrors: []string{"stats: stale"}}}
	sink := &stubSink{}
	srv := newTestServer(t, provider, sink, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, body %s", w.Code, w.Body.String())
	}
	var body struct {
		OK            bool     `json:"ok"`
		Version       string   `json:"version"`
		PID           int      `json:"pid"`
		ProjectKey    string   `json:"projectKey"`
		IsPollerOwner bool     `json:"isPollerOwner"`
		UptimeSec     int64    `json:"uptimeSec"`
		LoadErrors    []string `json:"loadErrors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Version != "v-test" || body.ProjectKey != "project-key" || !body.IsPollerOwner {
		t.Fatalf("health body = %+v", body)
	}
	if body.PID != os.Getpid() || body.UptimeSec < 3 {
		t.Fatalf("health pid/uptime = %d/%d", body.PID, body.UptimeSec)
	}
	if len(body.LoadErrors) != 1 || body.LoadErrors[0] != "stats: stale" {
		t.Fatalf("health loadErrors = %v", body.LoadErrors)
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("health без токена = %d, ожидался 401", w.Code)
	}
}

func TestHealthReportsSnapshotFailure(t *testing.T) {
	srv := newTestServer(t, stubProvider{err: errors.New("store down")}, &stubSink{}, NewEventBus(0))
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)))
	var body struct {
		OK         bool     `json:"ok"`
		LoadErrors []string `json:"loadErrors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK || len(body.LoadErrors) != 1 || !strings.Contains(body.LoadErrors[0], "store down") {
		t.Fatalf("health failure body = %+v", body)
	}
}

func TestSnapshot(t *testing.T) {
	snap := Snapshot{
		LiveWatches:       []domain.Watch{{ID: "w1", Status: domain.StatusWatching}},
		BlockerIncidents:  []domain.BlockerIncident{{ID: "b1", Status: domain.BlockerStatusOpen}},
		BoundOrchestrator: &domain.OrchestratorBinding{SessionID: "ses_1"},
		Escalations:       []string{"rung2: w1"},
		PointStats:        PointStats{Used: 7, Budget: 500},
		Metrics:           json.RawMessage(`{"poll_cycles_total":3}`),
		LastCycle:         LastCycle{WatchesUpdated: 2, DurationMs: 15},
		LoadErrors:        []string{"runtime_stats: broken"},
	}
	srv := newTestServer(t, stubProvider{snap: snap}, &stubSink{}, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body %s", w.Code, w.Body.String())
	}
	var got Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.LiveWatches) != 1 || got.LiveWatches[0].ID != "w1" {
		t.Fatalf("live watches = %+v", got.LiveWatches)
	}
	if len(got.BlockerIncidents) != 1 || got.BlockerIncidents[0].ID != "b1" {
		t.Fatalf("blockers = %+v", got.BlockerIncidents)
	}
	if got.BoundOrchestrator == nil || got.BoundOrchestrator.SessionID != "ses_1" {
		t.Fatalf("orchestrator = %+v", got.BoundOrchestrator)
	}
	if len(got.Escalations) != 1 || got.Escalations[0] != "rung2: w1" {
		t.Fatalf("escalations = %v", got.Escalations)
	}
	if got.PointStats != snap.PointStats || got.LastCycle != snap.LastCycle {
		t.Fatalf("stats/cycle = %+v / %+v", got.PointStats, got.LastCycle)
	}
	if !json.Valid(got.Metrics) || !strings.Contains(string(got.Metrics), "poll_cycles_total") {
		t.Fatalf("metrics = %s", got.Metrics)
	}
	if len(got.LoadErrors) != 1 || got.LoadErrors[0] != "runtime_stats: broken" {
		t.Fatalf("loadErrors = %v", got.LoadErrors)
	}
}

func TestSnapshotFailureIs500(t *testing.T) {
	srv := newTestServer(t, stubProvider{err: errors.New("no store")}, &stubSink{}, NewEventBus(0))
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("snapshot failure status = %d", w.Code)
	}
}

func readFrame(t *testing.T, r *bufio.Reader) map[string]string {
	t.Helper()
	frame := make(map[string]string)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return frame
		}
		if i := strings.Index(line, ": "); i > 0 {
			frame[line[:i]] = line[i+2:]
		}
	}
}

func connectEvents(t *testing.T, srv *Server, target string) (*http.Response, *bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	authorized(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		resp.Body.Close()
		cancel()
		t.Fatalf("Content-Type = %q", ct)
	}
	return resp, bufio.NewReader(resp.Body), cancel
}

func TestEventsReplayAndLive(t *testing.T) {
	bus := NewEventBus(10)
	for i := 1; i <= 3; i++ {
		bus.Publish(Event{Type: "cycle", Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))})
	}
	srv := newTestServer(t, stubProvider{}, &stubSink{}, bus)
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()

	resp, reader, cancel := connectEvents(t, srv, httpSrv.URL+"/api/v1/events?replay=2")
	defer cancel()
	defer resp.Body.Close()

	first := readFrame(t, reader)
	if first["id"] != "2" || first["event"] != "cycle" || !strings.Contains(first["data"], `"id":2`) {
		t.Fatalf("first replay frame = %v", first)
	}
	second := readFrame(t, reader)
	if second["id"] != "3" {
		t.Fatalf("second replay frame = %v", second)
	}

	bus.Publish(Event{Type: "human.decide", WatchID: "w1"})
	live := readFrame(t, reader)
	if live["id"] != "4" || live["event"] != "human.decide" {
		t.Fatalf("live frame = %v", live)
	}
}

func TestEventsLastEventID(t *testing.T) {
	bus := NewEventBus(10)
	bus.Publish(Event{Type: "a"})
	bus.Publish(Event{Type: "b"})
	bus.Publish(Event{Type: "c"})
	srv := newTestServer(t, stubProvider{}, &stubSink{}, bus)
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/api/v1/events", nil)
	authorized(req)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first := readFrame(t, reader)
	second := readFrame(t, reader)
	if first["id"] != "2" || second["id"] != "3" {
		t.Fatalf("replay frames = %v, %v", first, second)
	}
}

func TestEventsHeartbeat(t *testing.T) {
	previous := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	defer func() { heartbeatInterval = previous }()

	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()
	resp, reader, cancel := connectEvents(t, srv, httpSrv.URL+"/api/v1/events")
	defer cancel()
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(line) == ": heartbeat" {
			return
		}
	}
	t.Fatal("heartbeat не получен")
}

func decideRequest(body, origin string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/human/decide", strings.NewReader(body))
	authorized(r)
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestHumanDecide(t *testing.T) {
	sink := &stubSink{result: DecisionResult{DecisionID: "journal-9", Status: "watching/ready"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))
	body := `{"command":"approve","watchID":"w1","note":"ok","actor":"human"}`

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(body, "http://127.0.0.1:7432"))
	if w.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body %s", w.Code, w.Body.String())
	}
	var got DecisionResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Applied || got.DecisionID != "journal-9" || got.Command != "approve" || got.WatchID != "w1" {
		t.Fatalf("decide result = %+v", got)
	}
	if len(sink.calls) != 1 || sink.calls[0].Note != "ok" || sink.calls[0].Actor != "human" {
		t.Fatalf("sink calls = %+v", sink.calls)
	}

	// The same command is a cached reply: the sink must not run twice.
	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(body, "http://127.0.0.1:7432"))
	var repeat DecisionResult
	if err := json.Unmarshal(w.Body.Bytes(), &repeat); err != nil {
		t.Fatal(err)
	}
	if repeat.Applied || len(sink.calls) != 1 {
		t.Fatalf("idempotent repeat = %+v, calls %d", repeat, len(sink.calls))
	}
}

func TestHumanDecideGuards(t *testing.T) {
	sink := &stubSink{result: DecisionResult{DecisionID: "d"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))
	body := `{"command":"unblock","watchID":"w1"}`
	cases := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"нет токена", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/human/decide", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			return r
		}(), http.StatusUnauthorized},
		{"чужой origin", decideRequest(body, "http://evil.example"), http.StatusForbidden},
		{"не JSON", func() *http.Request {
			r := decideRequest(body, "")
			r.Header.Set("Content-Type", "text/plain")
			return r
		}(), http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, tc.req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, ожидался %d, body %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	if len(sink.calls) != 0 {
		t.Fatalf("sink вызван при отклонённых запросах: %+v", sink.calls)
	}

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(`{"command":"destroy","watchID":"w1"}`, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("неизвестная команда: status %d", w.Code)
	}
	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(`{"command":"cancel"}`, ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("пустой watchID: status %d", w.Code)
	}
}

func TestHumanDecideAppCredentials(t *testing.T) {
	sink := &stubSink{result: DecisionResult{DecisionID: "journal-9"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))
	srv.cfg.Credentials = AppCredentials{"app-reports": {"alice"}}

	// The permitted actor uses the same route and keeps the contract echo.
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(
		`{"command":"approve","watchID":"w1","actor":"alice","appKey":"app-reports","contract":"beacon.human-decide.v1"}`,
		"http://127.0.0.1:7432",
	))
	if w.Code != http.StatusOK {
		t.Fatalf("разрешённый actor: status %d, body %s", w.Code, w.Body.String())
	}
	var got DecisionResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Applied || got.Contract != HumanDecideContract {
		t.Fatalf("decide result = %+v", got)
	}
	if len(sink.calls) != 1 || sink.calls[0].Actor != "alice" {
		t.Fatalf("sink calls = %+v", sink.calls)
	}

	cases := []struct {
		name string
		body string
	}{
		{"чужой actor", `{"command":"approve","watchID":"w2","actor":"bob","appKey":"app-reports","contract":"beacon.human-decide.v1"}`},
		{"неизвестный ключ", `{"command":"approve","watchID":"w3","actor":"alice","appKey":"app-unknown","contract":"beacon.human-decide.v1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, decideRequest(tc.body, "http://127.0.0.1:7432"))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "ACTOR_NOT_PERMITTED") {
				t.Fatalf("body = %s", w.Body.String())
			}
		})
	}
	if len(sink.calls) != 1 {
		t.Fatalf("отклонённые команды дошли до sink: %+v", sink.calls)
	}

	// The version guard runs before the actor check and still wins.
	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(
		`{"command":"approve","watchID":"w4","actor":"bob","appKey":"app-reports","contract":"beacon.human-decide.v9"}`,
		"http://127.0.0.1:7432",
	))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "UNSUPPORTED_CONTRACT") {
		t.Fatalf("версия контракта: status %d, body %s", w.Code, w.Body.String())
	}
}

func TestHumanDecideAppCredentialsViaFakeClient(t *testing.T) {
	sink := &stubSink{result: DecisionResult{DecisionID: "journal-9"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))
	srv.cfg.Credentials = AppCredentials{"app-reports": {"alice"}}

	client, err := NewProjectionClient(ClientConn{Host: "127.0.0.1", Port: 7432, Token: "tok"},
		fakeDoer{fn: func(req *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, req)
			return w.Result(), nil
		}})
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := client.Decide(context.Background(), ClientDecision{
		Verb: "approve", Target: "w1", Actor: "alice", AppKey: "app-reports",
	})
	if err != nil {
		t.Fatalf("разрешённый actor через клиента: %v", err)
	}
	if !receipt.OK || receipt.Contract != HumanDecideContract {
		t.Fatalf("receipt = %+v", receipt)
	}

	if _, err := client.Decide(context.Background(), ClientDecision{
		Verb: "approve", Target: "w2", Actor: "bob", AppKey: "app-reports",
	}); err == nil {
		t.Fatal("чужой actor должен отвергаться через клиента")
	}
	if len(sink.calls) != 1 {
		t.Fatalf("sink calls = %+v", sink.calls)
	}
}

func TestStartWritesConnectionAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	srv, err := Start(Config{
		Addr:     "0.0.0.0:0",
		StateDir: dir,
		Version:  "v9",
		Token:    "fixed-token",
		Provider: stubProvider{},
		Sink:     &stubSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, connectionFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("connection.json mode = %o, ожидался 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cf connectionFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatal(err)
	}
	if cf.Host != "127.0.0.1" || cf.Port <= 0 || cf.Token != "fixed-token" || cf.PID != os.Getpid() || cf.Version != "v9" {
		t.Fatalf("connection file = %+v", cf)
	}

	base := "http://" + cf.Host + ":" + strconv.Itoa(cf.Port)
	resp, err := http.Get(base + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("health без токена = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer fixed-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("health = %d %s", resp.StatusCode, body)
	}

	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("connection.json не удалён после закрытия: %v", err)
	}
}

func TestCloseKeepsForeignConnectionFile(t *testing.T) {
	srv, err := Start(Config{
		StateDir: t.TempDir(),
		Provider: stubProvider{},
		Sink:     &stubSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(srv.cfg.StateDir, connectionFileName)
	foreign := connectionFile{Host: "127.0.0.1", Port: 9999, Token: "other", PID: srv.pid + 1}
	raw, _ := json.Marshal(foreign)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("чужой connection.json удалён: %v", err)
	}
	_ = os.Remove(path)
}

type historyProvider struct {
	stubProvider
	history SnapshotHistory
	err     error
}

func (p historyProvider) SnapshotHistory(context.Context) (SnapshotHistory, error) {
	return p.history, p.err
}

func getSnapshotBody(t *testing.T, srv *Server) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)))
	return w.Code, w.Body.Bytes()
}

func TestSnapshotIncludesOptionalHistory(t *testing.T) {
	closedAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	history := SnapshotHistory{
		RecentWatches: []WatchHistory{
			{ID: "w1", Repo: "refusedguy/beacon", PR: 7, HeadSHA: "abc", Outcome: OutcomeMerged, ClosedAt: closedAt},
			{ID: "w2", Repo: "refusedguy/beacon", PR: 8, HeadSHA: "def", Outcome: OutcomeEscalated, ClosedAt: closedAt.Add(-time.Hour), CloseReason: "timeout"},
		},
		HourlyStats: HourlyStats{Points: 12, Merges: 3, Blockers: 1},
	}
	srv := newTestServer(t, historyProvider{history: history}, &stubSink{}, NewEventBus(0))

	status, body := getSnapshotBody(t, srv)
	if status != http.StatusOK {
		t.Fatalf("snapshot status = %d, body %s", status, body)
	}
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.RecentWatches) != 2 || snap.RecentWatches[0].Outcome != OutcomeMerged || snap.RecentWatches[1].Outcome != OutcomeEscalated {
		t.Fatalf("recentWatches = %+v", snap.RecentWatches)
	}
	if snap.HourlyStats == nil || *snap.HourlyStats != history.HourlyStats {
		t.Fatalf("hourlyStats = %+v", snap.HourlyStats)
	}
	if snap.RecentWatchesTruncated {
		t.Fatal("truncation reported for a history that fits the cap")
	}
}

func TestSnapshotCapsHistoryAtThirty(t *testing.T) {
	watches := make([]WatchHistory, maxRecentWatches+5)
	for i := range watches {
		watches[i] = WatchHistory{ID: fmt.Sprintf("w%d", i), Outcome: OutcomeClosed}
	}
	srv := newTestServer(t, historyProvider{history: SnapshotHistory{RecentWatches: watches}}, &stubSink{}, NewEventBus(0))

	_, body := getSnapshotBody(t, srv)
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.RecentWatches) != maxRecentWatches {
		t.Fatalf("recentWatches length = %d, want %d", len(snap.RecentWatches), maxRecentWatches)
	}
	if snap.RecentWatches[0].ID != "w0" || snap.RecentWatches[maxRecentWatches-1].ID != fmt.Sprintf("w%d", maxRecentWatches-1) {
		t.Fatalf("history was not truncated from the front: %+v", snap.RecentWatches)
	}
	if !snap.RecentWatchesTruncated {
		t.Fatal("truncation was not reported to the consumer")
	}
}

func TestSnapshotHistoryFailureBecomesLoadError(t *testing.T) {
	srv := newTestServer(t, historyProvider{err: errors.New("store locked")}, &stubSink{}, NewEventBus(0))

	status, body := getSnapshotBody(t, srv)
	if status != http.StatusOK {
		t.Fatalf("snapshot status = %d, body %s", status, body)
	}
	var snap struct {
		RecentWatches []WatchHistory `json:"recentWatches"`
		HourlyStats   *HourlyStats   `json:"hourlyStats"`
		LoadErrors    []string       `json:"loadErrors"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.LoadErrors) != 1 || !strings.Contains(snap.LoadErrors[0], "history: store locked") {
		t.Fatalf("loadErrors = %v", snap.LoadErrors)
	}
	if snap.RecentWatches != nil || snap.HourlyStats != nil {
		t.Fatalf("history fields present after failure: %+v / %+v", snap.RecentWatches, snap.HourlyStats)
	}
}

func TestSnapshotWithoutHistoryKeepsOldShape(t *testing.T) {
	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))

	_, body := getSnapshotBody(t, srv)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["recentWatches"]; ok {
		t.Fatal("recentWatches present without a history provider")
	}
	if _, ok := raw["hourlyStats"]; ok {
		t.Fatal("hourlyStats present without a history provider")
	}
	if _, ok := raw["recentWatchesTruncated"]; ok {
		t.Fatal("recentWatchesTruncated present without a history provider")
	}
}

func TestStartGeneratesRandomToken(t *testing.T) {
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first, err := Start(Config{StateDir: firstDir, Provider: stubProvider{}, Sink: &stubSink{}})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Start(Config{StateDir: secondDir, Provider: stubProvider{}, Sink: &stubSink{}})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	read := func(dir string) connectionFile {
		raw, err := os.ReadFile(filepath.Join(dir, connectionFileName))
		if err != nil {
			t.Fatal(err)
		}
		var cf connectionFile
		if err := json.Unmarshal(raw, &cf); err != nil {
			t.Fatal(err)
		}
		return cf
	}
	a, b := read(firstDir), read(secondDir)
	if a.Token == "" || b.Token == "" || a.Token == b.Token {
		t.Fatalf("токены = %q и %q", a.Token, b.Token)
	}
	if len(a.Token) != 48 {
		t.Fatalf("длина токена = %d, ожидалась 48", len(a.Token))
	}
}

func TestVersionEndpoint(t *testing.T) {
	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("version status = %d, body %s", w.Code, w.Body.String())
	}
	var body VersionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Version != "v-test" || body.PID != os.Getpid() || body.ProjectKey != "project-key" {
		t.Fatalf("version body = %+v", body)
	}
	if body.UptimeSec < 3 || !body.StartedAt.Equal(srv.cfg.StartedAt) {
		t.Fatalf("version uptime/startedAt = %d/%s", body.UptimeSec, body.StartedAt)
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("version без токена = %d, ожидался 401", w.Code)
	}
}

func TestMetricsActiveTasksMatchesLiveWatches(t *testing.T) {
	snap := Snapshot{LiveWatches: []domain.Watch{
		{ID: "w1", Status: domain.StatusWatching},
		{ID: "w2", Status: domain.StatusWatching},
	}}
	srv := newTestServer(t, stubProvider{snap: snap}, &stubSink{}, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body %s", w.Code, w.Body.String())
	}
	var body OpsMetrics
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.ActiveTasks != len(snap.LiveWatches) {
		t.Fatalf("metrics = %+v, active_tasks должен быть %d", body, len(snap.LiveWatches))
	}
	if body.WakesTotal != 0 {
		t.Fatalf("wakes_total = %d, ожидался 0", body.WakesTotal)
	}
}

func TestMetricsAfterWake(t *testing.T) {
	snap := Snapshot{LiveWatches: []domain.Watch{{ID: "w1", Status: domain.StatusWatching}}}
	srv := newTestServer(t, stubProvider{snap: snap}, &stubSink{}, NewEventBus(0))
	srv.ObserveWake(120 * time.Millisecond)
	srv.ObserveWake(180 * time.Millisecond)

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, body %s", w.Code, w.Body.String())
	}
	var body OpsMetrics
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WakesTotal != 2 || body.WakeLatencyMs != 150 || body.WakeLatencyMaxMs != 180 || body.WakeLatencyLastMs != 180 {
		t.Fatalf("wake metrics = %+v", body)
	}
	if body.ActiveTasks != 1 {
		t.Fatalf("active_tasks = %d, ожидался 1", body.ActiveTasks)
	}
}

func TestMetricsSnapshotFailure(t *testing.T) {
	srv := newTestServer(t, stubProvider{err: errors.New("store down")}, &stubSink{}, NewEventBus(0))
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("metrics при ошибке провайдера = %d, ожидался 500", w.Code)
	}
}

func TestAuditActorIngress(t *testing.T) {
	sink := &stubSink{result: DecisionResult{DecisionID: "journal-9"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))

	body := `{"command":"approve","watchID":"w1","actor":"human"}`
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(body, "http://127.0.0.1:7432"))
	if w.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("audit status = %d, body %s", w.Code, w.Body.String())
	}
	var page AuditPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Entries) != 1 {
		t.Fatalf("audit page = %+v", page)
	}
	got := page.Entries[0]
	if got.Actor != "human" || got.Ingress != ingressHTTPAPI || got.Command != "approve" ||
		got.WatchID != "w1" || got.DecisionID != "journal-9" || got.At.IsZero() {
		t.Fatalf("audit entry = %+v", got)
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/audit?actor=other", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("audit filter status = %d", w.Code)
	}
	var filtered AuditPage
	if err := json.Unmarshal(w.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 0 || len(filtered.Entries) != 0 {
		t.Fatalf("audit filter page = %+v", filtered)
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("audit без токена = %d, ожидался 401", w.Code)
	}
}

func TestAuditRedaction(t *testing.T) {
	ghp := "ghp_" + strings.Repeat("a", 36)
	tg := "123456789:" + strings.Repeat("b", 35)

	// The sink echoes a token into the command so the audit record carries a
	// secret in every free-text field.
	sink := &stubSink{result: DecisionResult{DecisionID: ghp, Command: "approve " + tg}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))

	// A command that carries a token is rejected. The error must not echo it.
	body := `{"command":"approve ` + tg + `","watchID":"` + tg + `","actor":"` + ghp + `"}`
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(body, "http://127.0.0.1:7432"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("decide status = %d, body %s", w.Code, w.Body.String())
	}
	var errBody errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Code != string(domain.CodeInvalidInput) {
		t.Fatalf("error code = %q, body %s", errBody.Code, w.Body.String())
	}
	if !strings.Contains(errBody.Message, "[REDACTED]") || strings.Contains(errBody.Message, tg) {
		t.Fatalf("token leaked in 400 body: %s", w.Body.String())
	}

	// A valid decision records one audit entry with the secrets still in it.
	body = `{"command":"approve","watchID":"` + tg + `","actor":"` + ghp + `"}`
	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(body, "http://127.0.0.1:7432"))
	if w.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("audit status = %d, body %s", w.Code, w.Body.String())
	}
	var page AuditPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Entries) != 1 {
		t.Fatalf("audit page = %+v", page)
	}

	got := page.Entries[0]
	if strings.Contains(got.Actor, "ghp_") || strings.Contains(got.Command, tg) ||
		strings.Contains(got.WatchID, tg) || strings.Contains(got.DecisionID, "ghp_") {
		t.Fatalf("secret survived in audit entry: %+v", got)
	}
	if got.Actor != "[REDACTED]" || got.DecisionID != "[REDACTED]" {
		t.Fatalf("token fields not redacted: %+v", got)
	}
	if got.Command != "approve [REDACTED]" || got.WatchID != "[REDACTED]" {
		t.Fatalf("command and watchID not redacted: %+v", got)
	}
	if got.Ingress != ingressHTTPAPI || got.At.IsZero() {
		t.Fatalf("ingress or time changed: %+v", got)
	}
}

func TestAuditForbiddenActorRedaction(t *testing.T) {
	ghp := "ghp_" + strings.Repeat("a", 36)
	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))
	srv.cfg.Credentials = AppCredentials{"app-reports": {"alice"}}

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(
		`{"command":"approve","watchID":"w1","actor":"`+ghp+`","appKey":"app-reports","contract":"beacon.human-decide.v1"}`,
		"http://127.0.0.1:7432",
	))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "ACTOR_NOT_PERMITTED") {
		t.Fatalf("body = %s", body)
	}
	if strings.Contains(body, ghp) {
		t.Fatalf("token leaked in 403 body: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("missing redaction marker in 403 body: %s", body)
	}
}

func TestTypedEventClosedNameSet(t *testing.T) {
	for _, name := range []EventName{EventSnapshot, EventWatchPatch, EventObligationPatch, EventVerdictPatch} {
		if err := validateEventName(name); err != nil {
			t.Fatalf("name %q rejected: %v", name, err)
		}
	}

	unknown := EventName("watch.destroyed")
	bus := NewEventBus(10)
	patch := &PatchPayload{
		Contract: EventContract,
		Ops:      []PatchOp{{Op: "replace", Path: "/status", Value: json.RawMessage(`"ready"`)}},
	}
	if err := bus.PublishTyped(unknown, patch); !errors.Is(err, ErrUnknownEventName) {
		t.Fatalf("PublishTyped unknown name = %v", err)
	}
	if _, err := DecodeTypedEvent(Event{Type: string(unknown), Payload: json.RawMessage(`{}`)}); !errors.Is(err, ErrUnknownEventName) {
		t.Fatalf("DecodeTypedEvent unknown name = %v", err)
	}
	if got := len(bus.Last(10)); got != 0 {
		t.Fatalf("unknown name published %d frames", got)
	}
}

func TestTypedEventPayloadValidation(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want error
	}{
		{"snapshot wrong contract", Event{Type: string(EventSnapshot), Payload: json.RawMessage(`{"contract":"v9","snapshot":{}}`)}, ErrInvalidPayload},
		{"snapshot malformed", Event{Type: string(EventSnapshot), Payload: json.RawMessage(`{`)}, ErrInvalidPayload},
		{"patch wrong contract", Event{Type: string(EventWatchPatch), Payload: json.RawMessage(`{"contract":"v9","ops":[{"op":"add","path":"/x"}]}`)}, ErrInvalidPayload},
		{"patch no ops", Event{Type: string(EventWatchPatch), Payload: json.RawMessage(`{"contract":"beacon.events.v1","ops":[]}`)}, ErrInvalidPayload},
		{"patch bad op", Event{Type: string(EventVerdictPatch), Payload: json.RawMessage(`{"contract":"beacon.events.v1","ops":[{"op":"explode","path":"/x"}]}`)}, ErrInvalidPayload},
		{"patch empty path", Event{Type: string(EventObligationPatch), Payload: json.RawMessage(`{"contract":"beacon.events.v1","ops":[{"op":"add","path":"  "}]}`)}, ErrInvalidPayload},
		{"patch malformed", Event{Type: string(EventWatchPatch), Payload: json.RawMessage(`{`)}, ErrInvalidPayload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeTypedEvent(tc.ev); !errors.Is(err, tc.want) {
				t.Fatalf("DecodeTypedEvent = %v, want %v", err, tc.want)
			}
		})
	}

	bus := NewEventBus(10)
	valid := []struct {
		name string
		ev   Event
	}{
		{"snapshot", Event{Type: string(EventSnapshot), Payload: json.RawMessage(`{"contract":"beacon.events.v1","snapshot":{"liveWatches":[]}}`)}},
		{"patch", Event{Type: string(EventVerdictPatch), Payload: json.RawMessage(`{"contract":"beacon.events.v1","baseID":1,"ops":[{"op":"replace","path":"/liveWatches/0/status","value":"ready"}]}`)}},
	}
	for _, tc := range valid {
		if err := bus.PublishTyped(EventName(tc.ev.Type), tc.ev.Payload); err != nil {
			t.Fatalf("PublishTyped %s: %v", tc.name, err)
		}
	}

	frames := bus.Last(2)
	gotSnapshot, err := DecodeTypedEvent(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	if gotSnapshot.Name != EventSnapshot || gotSnapshot.Contract != EventContract || gotSnapshot.Snapshot == nil {
		t.Fatalf("snapshot = %+v", gotSnapshot)
	}
	gotPatch, err := DecodeTypedEvent(frames[1])
	if err != nil {
		t.Fatal(err)
	}
	if gotPatch.Name != EventVerdictPatch || gotPatch.Patch == nil || gotPatch.Patch.BaseID != 1 {
		t.Fatalf("patch = %+v", gotPatch)
	}
	if len(gotPatch.Patch.Ops) != 1 || gotPatch.Patch.Ops[0].Op != "replace" {
		t.Fatalf("patch ops = %+v", gotPatch.Patch.Ops)
	}
}

func TestTypedEventSnapshotCadence(t *testing.T) {
	previous := maxPatchesPerSnapshot
	maxPatchesPerSnapshot = 2
	defer func() { maxPatchesPerSnapshot = previous }()

	bus := NewEventBus(20)
	snap := SnapshotPayload{Contract: EventContract, Snapshot: Snapshot{}}
	patch := func() *PatchPayload {
		return &PatchPayload{
			Contract: EventContract,
			Ops:      []PatchOp{{Op: "replace", Path: "/status", Value: json.RawMessage(`"ready"`)}},
		}
	}
	if err := bus.PublishTyped(EventSnapshot, snap); err != nil {
		t.Fatal(err)
	}
	base := bus.LastSnapshotID()
	if base != 1 {
		t.Fatalf("lastSnapshotID = %d, want 1", base)
	}
	for i := 0; i < maxPatchesPerSnapshot; i++ {
		if err := bus.PublishTyped(EventWatchPatch, patch()); err != nil {
			t.Fatalf("patch %d: %v", i, err)
		}
	}
	if err := bus.PublishTyped(EventWatchPatch, patch()); !errors.Is(err, ErrSnapshotDue) {
		t.Fatalf("patch after cap = %v, want %v", err, ErrSnapshotDue)
	}

	last, err := DecodeTypedEvent(bus.Last(1)[0])
	if err != nil {
		t.Fatal(err)
	}
	if last.Patch == nil || last.Patch.BaseID != base {
		t.Fatalf("patch anchor = %+v, want %d", last.Patch, base)
	}

	if err := bus.PublishTyped(EventSnapshot, snap); err != nil {
		t.Fatal(err)
	}
	if err := bus.PublishTyped(EventVerdictPatch, patch()); err != nil {
		t.Fatal(err)
	}
	if got := bus.LastSnapshotID(); got != base+3 {
		t.Fatalf("lastSnapshotID after reset = %d, want %d", got, base+3)
	}
}

func TestEventsReplayAfterTypedPatch(t *testing.T) {
	bus := NewEventBus(10)
	if err := bus.PublishTyped(EventSnapshot, SnapshotPayload{Contract: EventContract, Snapshot: Snapshot{}}); err != nil {
		t.Fatal(err)
	}
	if err := bus.PublishTyped(EventWatchPatch, &PatchPayload{
		Contract: EventContract,
		Ops:      []PatchOp{{Op: "replace", Path: "/liveWatches/0/status", Value: json.RawMessage(`"ready"`)}},
	}); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, stubProvider{}, &stubSink{}, bus)
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/api/v1/events", nil)
	authorized(req)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	frame := readFrame(t, bufio.NewReader(resp.Body))
	if frame["id"] != "2" || frame["event"] != string(EventWatchPatch) {
		t.Fatalf("replayed frame = %v", frame)
	}
	var envelope Event
	if err := json.Unmarshal([]byte(frame["data"]), &envelope); err != nil {
		t.Fatal(err)
	}
	typed, err := DecodeTypedEvent(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if typed.ID != 2 || typed.Patch == nil || typed.Patch.BaseID != 1 {
		t.Fatalf("typed replay = %+v", typed)
	}
}

func TestExtraRoutesShareBearerGateway(t *testing.T) {
	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))
	srv.cfg.ExtraRoutes = map[string]http.Handler{
		"/probe": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
	}

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/probe", nil)))
	if w.Code != http.StatusTeapot {
		t.Fatalf("extra route status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("extra route without token = %d, want 401", w.Code)
	}
}

func TestMCPPathIsNotMountedByDefault(t *testing.T) {
	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))
	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodPost, "/mcp", nil)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /mcp status = %d, want 404", w.Code)
	}
}
