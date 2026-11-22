package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/mcp"
)

// fakeInspectStore is the read-only slice of the state store inspect uses. It
// returns a fixed binding so the tests cover both the bound and unbound paths
// without opening a real SQLite handle.
type fakeInspectStore struct {
	binding     domain.OrchestratorBinding
	bound       bool
	projectKeys []string
	watches     []domain.Watch
	blockers    []domain.BlockerIncident
	stats       []byte
	statsOK     bool
}

func (s *fakeInspectStore) LiveWatches() ([]domain.Watch, error) { return s.watches, nil }

func (s *fakeInspectStore) ActiveBlockers() ([]domain.BlockerIncident, error) {
	return s.blockers, nil
}

func (s *fakeInspectStore) BoundOrchestrator(projectKey string) (domain.OrchestratorBinding, bool, error) {
	s.projectKeys = append(s.projectKeys, projectKey)
	return s.binding, s.bound, nil
}

func (s *fakeInspectStore) LoadRuntimeStats() ([]byte, bool, error) {
	return s.stats, s.statsOK, nil
}

func installFakeInspectStore(t *testing.T, s *fakeInspectStore) {
	t.Helper()
	old := makeInspectStore
	t.Cleanup(func() { makeInspectStore = old })
	SetInspectStore(func(*CLI) (inspectStore, func() error, error) {
		return s, func() error { return nil }, nil
	})
	t.Setenv(beaconStateDirEnv, t.TempDir())
}

func boundInspectStore() *fakeInspectStore {
	return &fakeInspectStore{
		binding: domain.OrchestratorBinding{
			ProjectKey: "project-key",
			SessionID:  "ses_bound",
			Adapter:    "opencode",
			BoundAt:    time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		bound: true,
	}
}

func TestInspectTableShowsBoundOrchestrator(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	s := boundInspectStore()
	installFakeInspectStore(t, s)

	if code := c.Run([]string{"inspect"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	want := "Orchestrator: ses_bound (opencode, 2026-01-02T03:04:05Z)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout missing %q: %q", want, out.String())
	}
	if strings.Contains(out.String(), "WARNING: orchestrator not bound") {
		t.Errorf("bound orchestrator still warned: %q", out.String())
	}
	key := orchestratorProjectKey()
	if len(s.projectKeys) != 1 || s.projectKeys[0] != key {
		t.Errorf("BoundOrchestrator keys = %v, want [%s]", s.projectKeys, key)
	}
}

func TestInspectTableWarnsWhenOrchestratorUnbound(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	installFakeInspectStore(t, &fakeInspectStore{})

	if code := c.Run([]string{"inspect"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	want := "WARNING: orchestrator not bound; run beacon orchestrator bind --session <ses_>"
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout missing %q: %q", want, out.String())
	}
	if strings.Contains(out.String(), "Orchestrator:") {
		t.Errorf("unbound orchestrator printed a binding: %q", out.String())
	}
}

func TestInspectJSONIncludesBoundOrchestrator(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	s := boundInspectStore()
	installFakeInspectStore(t, s)

	if code := c.Run([]string{"inspect", "--json"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	var view inspectView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("decode inspect JSON: %v; output: %q", err, out.String())
	}
	if view.Orchestrator == nil {
		t.Fatalf("orchestrator is empty: %q", out.String())
	}
	if view.Orchestrator.SessionID != "ses_bound" || view.Orchestrator.Adapter != "opencode" {
		t.Errorf("orchestrator = %+v, want ses_bound/opencode", view.Orchestrator)
	}
	if !view.Orchestrator.BoundAt.Equal(s.binding.BoundAt) {
		t.Errorf("boundAt = %s, want %s", view.Orchestrator.BoundAt, s.binding.BoundAt)
	}
}

func TestInspectJSONShowsTypedGraphReady(t *testing.T) {
	tests := []struct {
		name       string
		loadErrors []string
	}{
		{"fresh", []string{"graphReady: true"}},
		{"stale", []string{"graphReady: false"}},
		{"no reader", nil},
		{"read error", []string{"graph index: boom"}},
		{"read error wins over stale marker", []string{"graphReady: false", "graph index: boom"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stats := mcp.StatsOutput{LoadErrors: tc.loadErrors}
			raw, err := json.Marshal(stats)
			if err != nil {
				t.Fatalf("marshal stats: %v", err)
			}
			want := stats.GraphReady()

			c, out, errOut, _ := t06NewCLI(t, nil)
			installFakeInspectStore(t, &fakeInspectStore{stats: raw, statsOK: true})
			if code := c.Run([]string{"inspect", "--json"}); code != 0 {
				t.Fatalf("code = %d, stderr: %s", code, errOut.String())
			}

			var view inspectView
			if err := json.Unmarshal(out.Bytes(), &view); err != nil {
				t.Fatalf("decode inspect JSON: %v; output: %q", err, out.String())
			}
			if view.GraphReady == nil {
				t.Fatalf("graphReady field missing: %q", out.String())
			}
			if view.GraphReady.Ready != want.Ready || view.GraphReady.Source != string(want.Source) {
				t.Errorf("graphReady = %+v, want %+v", view.GraphReady, want)
			}
		})
	}
}

func TestInspectJSONKeepsLegacyGraphReadyMarker(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	installFakeInspectStore(t, &fakeInspectStore{
		stats:   []byte(`{"loadErrors":["graphReady: true"]}`),
		statsOK: true,
	})

	if code := c.Run([]string{"inspect", "--json"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `graphReady: true`) {
		t.Errorf("legacy marker lost from runtimeStats: %q", out.String())
	}
}

func TestInspectJSONEmptyBlockersIsArray(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	installFakeInspectStore(t, &fakeInspectStore{})

	if code := c.Run([]string{"inspect", "--json"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("decode inspect JSON: %v; output: %q", err, out.String())
	}
	blockers, ok := raw["blockers"]
	if !ok {
		t.Fatalf("blockers field missing: %q", out.String())
	}
	if got := strings.TrimSpace(string(blockers)); got != "[]" {
		t.Errorf("blockers = %s, want []", got)
	}
}

// writeReadyTestCard пишет карточку со всеми обязательными полями, чтобы
// taskstore.LoadCards разобрал её без ошибок.
func writeReadyTestCard(t *testing.T, dir, id, status, deps, extra string) {
	t.Helper()
	text := "# " + id + ". test\n\n" +
		"Тип: task\n" +
		"Статус: " + status + "\n" +
		"Фаза: P1\n" +
		"Владелец файлов: -\n" +
		"Зависимости: " + deps + "\n" +
		"Ссылка: -\n" +
		"Цель: -\n" +
		"Контекст: -\n" +
		"Шаги: -\n" +
		"Проверка: -\n" +
		"Приёмка: -\n" +
		"Агент: -\n" +
		"Время: -\n" + extra + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0o644); err != nil {
		t.Fatalf("write card %s: %v", id, err)
	}
}

// readyFixtureDir создаёт четыре карточки готовой очереди и четырёх
// блокировщиков. T9.1 готов, потому что его блокировщики закрыт и закреплён,
// T9.4 заблокирован двумя открытыми. parent-child и related не должны попасть
// в объяснение ни одной карточки.
func readyFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeReadyTestCard(t, dir, "T9.1", "todo", "T9.5, T9.6, T9.5", "part-of T9.4\nsame-as T9.4\nrelated T9.4\n")
	writeReadyTestCard(t, dir, "T9.2", "todo", "нет", "")
	writeReadyTestCard(t, dir, "T9.3", "todo", "T9.7", "")
	writeReadyTestCard(t, dir, "T9.4", "todo", "T9.7, T9.8, T9.7", "")
	writeReadyTestCard(t, dir, "T9.5", "done", "нет", "")
	writeReadyTestCard(t, dir, "T9.6", "todo", "нет", "Закреплена: true\n")
	writeReadyTestCard(t, dir, "T9.7", "in_progress", "нет", "")
	writeReadyTestCard(t, dir, "T9.8", "in_progress", "нет", "")
	return dir
}

func readyIDs(deps []readyDep) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.ID)
	}
	return out
}

func TestBuildReadyExplainSeparatesDependencies(t *testing.T) {
	dir := readyFixtureDir(t)
	view, err := buildReadyExplain(dir)
	if err != nil {
		t.Fatalf("buildReadyExplain: %v", err)
	}
	if len(view.Ready) != 2 || len(view.Blocked) != 2 {
		t.Fatalf("ready=%d blocked=%d, want 2 and 2: %+v", len(view.Ready), len(view.Blocked), view)
	}
	if view.Ready[0].ID != "T9.1" || view.Ready[1].ID != "T9.2" {
		t.Errorf("ready IDs = %s, want [T9.1 T9.2]", []string{view.Ready[0].ID, view.Ready[1].ID})
	}
	if view.Blocked[0].ID != "T9.3" || view.Blocked[1].ID != "T9.4" {
		t.Errorf("blocked IDs = %s, want [T9.3 T9.4]", []string{view.Blocked[0].ID, view.Blocked[1].ID})
	}

	first := view.Ready[0]
	if got := readyIDs(first.ResolvedBlockers); len(got) != 1 || got[0] != "T9.5" {
		t.Errorf("resolved = %v, want [T9.5]", got)
	}
	if got := readyIDs(first.PinnedDependencies); len(got) != 1 || got[0] != "T9.6" {
		t.Errorf("pinned = %v, want [T9.6]", got)
	}
	if len(first.OpenDependencies) != 0 {
		t.Errorf("open = %v, want none", readyIDs(first.OpenDependencies))
	}
	if got := readyIDs(view.Blocked[1].OpenDependencies); len(got) != 2 || got[0] != "T9.7" || got[1] != "T9.8" {
		t.Errorf("blocked open = %v, want [T9.7 T9.8] and no duplicates", got)
	}

	// parent-child (part-of) и related не попали ни в один список.
	for _, e := range append(view.Ready, view.Blocked...) {
		for _, list := range [][]readyDep{e.ResolvedBlockers, e.PinnedDependencies, e.OpenDependencies} {
			for _, d := range list {
				if d.ID == "T9.4" {
					t.Errorf("non-gating edge %s landed in %s", d.ID, e.ID)
				}
			}
		}
	}
}

func TestBuildReadyExplainDeterministic(t *testing.T) {
	dir := readyFixtureDir(t)
	first, err := buildReadyExplain(dir)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := buildReadyExplain(dir)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	a, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	b, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("ready JSON is not byte-stable:\n%s\n%s", a, b)
	}
}

func TestRunTaskReadyJSON(t *testing.T) {
	dir := readyFixtureDir(t)
	cfg := taskcli.Config{TasksDir: dir, Root: dir}
	var out, errOut bytes.Buffer
	code := TaskCommandWithConfig([]string{"ready", "--format", "json"}, &out, &errOut, cfg)
	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	var view readyExplainView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("decode ready JSON: %v; output: %q", err, out.String())
	}
	if len(view.Ready) != 2 || len(view.Blocked) != 2 {
		t.Fatalf("ready=%d blocked=%d, want 2 and 2", len(view.Ready), len(view.Blocked))
	}
}

func TestInspectReadyQuery(t *testing.T) {
	c, out, errOut, _ := t06NewCLI(t, nil)
	installFakeInspectStore(t, &fakeInspectStore{})
	dir := readyFixtureDir(t)
	view, err := buildReadyExplain(dir)
	if err != nil {
		t.Fatalf("buildReadyExplain: %v", err)
	}
	old := loadReadyExplainForInspect
	t.Cleanup(func() { loadReadyExplainForInspect = old })
	loadReadyExplainForInspect = func() (readyExplainView, error) { return view, nil }

	if code := c.Run([]string{"inspect", "--ready", "--json"}); code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, errOut.String())
	}
	var got inspectView
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode inspect JSON: %v; output: %q", err, out.String())
	}
	if got.Ready == nil || len(got.Ready.Ready) != 2 || len(got.Ready.Blocked) != 2 {
		t.Fatalf("ready field = %+v, want 2 ready and 2 blocked", got.Ready)
	}
}
