package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type w68Call struct {
	dir  string
	name string
	args []string
	env  []string
}

func w68CLI(t *testing.T, respond func(w68Call) runResult) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]w68Call) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	calls := new([]w68Call)
	c := &CLI{
		out:    out,
		errOut: errOut,
		root:   t.TempDir(),
		run: func(dir, name string, args, env []string) runResult {
			call := w68Call{dir, name, append([]string(nil), args...), append([]string(nil), env...)}
			*calls = append(*calls, call)
			if respond == nil {
				return runResult{}
			}
			return respond(call)
		},
	}
	return c, out, errOut, calls
}

// w68Service replaces the registration reader and returns a pointer to the
// path it was asked to read.
func w68Service(t *testing.T, data []byte, err error) *string {
	t.Helper()
	prev := readServiceFile
	asked := new(string)
	readServiceFile = func(path string) ([]byte, error) {
		*asked = path
		return data, err
	}
	t.Cleanup(func() { readServiceFile = prev })
	return asked
}

const w68AgentList = `[
  {"id":"build","name":"Build","mode":"primary"},
  {"id":"reviewer","name":"Reviewer","mode":"subagent"},
  {"id":"general","name":"General","mode":"all"}
]`

const w68Registration = `{"url":"http://127.0.0.1:4096","pid":4242,"version":"2.3.0"}`

func TestW68AgentsTableFiltersAndUsesRunner(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	asked := w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, calls := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов раннера %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "opencode" || !reflect.DeepEqual(call.args, []string{"api", "app.agents"}) {
		t.Fatalf("не тот вызов: %+v", call)
	}
	if call.dir != c.root || len(call.env) != 0 {
		t.Fatalf("неверный каталог или окружение: %+v", call)
	}
	if want := filepath.Join("state", "opencode", "service.json"); !strings.HasSuffix(*asked, want) {
		t.Fatalf("читался путь %q, ожидался с суффиксом %q", *asked, want)
	}

	for _, want := range []string{"ID", "NAME", "MODE", "reviewer", "Reviewer", "subagent", "general", "all"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("в таблице нет %q: %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "primary") || strings.Contains(out.String(), "Build") {
		t.Fatalf("агент primary попал в таблицу: %q", out.String())
	}
}

func TestW68AgentsJSON(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("stdout не JSON: %q", out.String())
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []agentRow{
		{ID: "reviewer", Name: "Reviewer", Mode: "subagent"},
		{ID: "general", Name: "General", Mode: "all"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("строки %+v, ожидались %+v", got, want)
	}
}

func TestW68AgentsGroupedResponse(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	grouped := `{
	  "primary": [{"id":"build","name":"Build"}],
	  "subagent": [{"id":"reviewer","name":"Reviewer"}],
	  "all": [{"id":"general","name":"General"}]
	}`
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(grouped)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []agentRow{
		{ID: "general", Name: "General", Mode: "all"},
		{ID: "reviewer", Name: "Reviewer", Mode: "subagent"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("строки %+v, ожидались %+v", got, want)
	}
}

func TestW68AgentsServiceDown(t *testing.T) {
	w68Service(t, nil, os.ErrNotExist)
	c, out, errOut, calls := w68CLI(t, nil)

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "фоновый сервис не запущен") ||
		!strings.Contains(errOut.String(), "opencode service start") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("раннер не должен вызываться: %+v", *calls)
	}
}

func TestW68AgentsBrokenRegistration(t *testing.T) {
	w68Service(t, []byte("{не json"), nil)
	c, _, errOut, calls := w68CLI(t, nil)

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "фоновый сервис не запущен") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("раннер не должен вызываться: %+v", *calls)
	}
}

func TestW68AgentsRunnerFailure(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 2, stderr: []byte("connect ECONNREFUSED\n")}
	})

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "ECONNREFUSED") {
		t.Fatalf("нет причины отказа: %q", errOut.String())
	}
}

func TestW68AgentsMalformedAPIResponse(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, _, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte("not json")}
	})

	if code := c.runAgents(nil); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if !strings.Contains(errOut.String(), "не удалось разобрать") {
		t.Fatalf("нет ошибки разбора: %q", errOut.String())
	}
}

func TestW68AgentsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"неизвестный флаг", []string{"--bogus"}},
		{"неизвестный формат", []string{"--format", "yaml"}},
		{"лишний аргумент", []string{"extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w68Service(t, []byte(w68Registration), nil)
			c, out, errOut, calls := w68CLI(t, nil)

			if code := c.runAgents(tc.args); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr пустой")
			}
			if len(*calls) != 0 {
				t.Fatalf("раннер не должен вызываться: %+v", *calls)
			}
		})
	}
}

func TestW68AgentsEmptyListIsValidJSON(t *testing.T) {
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(`[{"id":"build","name":"Build","mode":"primary"}]`)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("пустой список: %q, ожидался []", got)
	}
}

// w236SessionList is the `opencode api session.list` envelope from ADR 0060.
// The same agent appears twice to check that rows are deduplicated.
const w236SessionList = `{
  "data": [
    {"id":"ses-1","parentID":"ses-0","agent":"reviewer","title":"rev"},
    {"id":"ses-2","parentID":"ses-0","agent":"general","title":"gen"},
    {"id":"ses-3","parentID":"ses-0","agent":"reviewer","title":"rev again"}
  ]
}`

func TestW236AgentsRowsFromSessionListAgents(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	respond := func(call w68Call) runResult {
		if call.name == "opencode" && len(call.args) == 2 && call.args[1] == "app.agents" {
			return runResult{code: 1, stderr: []byte("Operation not found: app.agents\n")}
		}
		return runResult{stdout: []byte(w236SessionList)}
	}

	c, out, errOut, calls := w68CLI(t, respond)
	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("вызовов раннера %d, ожидалось 2: %+v", len(*calls), *calls)
	}
	if (*calls)[0].args[1] != "app.agents" || (*calls)[1].args[1] != "session.list" {
		t.Fatalf("источники агентов: %+v", *calls)
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []agentRow{
		{ID: "general", Name: "general"},
		{ID: "reviewer", Name: "reviewer"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("строки %+v, ожидались %+v", got, want)
	}

	c, out, errOut, _ = w68CLI(t, respond)
	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("таблица: код выхода %d, stderr %q", code, errOut.String())
	}
	for _, needle := range []string{"ID", "NAME", "MODE", "STALE", "ATTENTION", "general", "reviewer"} {
		if !strings.Contains(out.String(), needle) {
			t.Fatalf("в таблице нет %q: %q", needle, out.String())
		}
	}
}

func TestW92SandboxCloneArgsUsesReference(t *testing.T) {
	got := sandboxCloneArgs("/srv/beacon.git", "/work/agents/a1")
	want := []string{"clone", "--reference", "/srv/beacon.git", "/srv/beacon.git", "/work/agents/a1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("аргументы клона %q, ожидались %q", got, want)
	}
}

func TestW92ProvisionSandboxRunsGitWithReference(t *testing.T) {
	c, out, errOut, calls := w68CLI(t, func(w68Call) runResult {
		return runResult{}
	})

	if code := c.provisionSandbox("/srv/beacon.git", "/work/agents/a1"); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов раннера %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "git" {
		t.Fatalf("раннер вызвал %q, ожидался git", call.name)
	}
	want := []string{"clone", "--reference", "/srv/beacon.git", "/srv/beacon.git", "/work/agents/a1"}
	if !reflect.DeepEqual(call.args, want) {
		t.Fatalf("аргументы %q, ожидались %q", call.args, want)
	}
	if call.dir != c.root || len(call.env) != 0 {
		t.Fatalf("неверный каталог или окружение: %+v", call)
	}
}

func TestW92ProvisionSandboxCloneFailure(t *testing.T) {
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 128, stderr: []byte("fatal: destination path already exists\n")}
	})

	if code := c.provisionSandbox("/srv/beacon.git", "/work/agents/a1"); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "destination path already exists") ||
		!strings.Contains(errOut.String(), "--reference") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
}

func TestW92ProvisionSandboxRejectsEmptyPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		dest   string
	}{
		{"пустой источник", "", "/work/agents/a1"},
		{"пробельный источник", "  ", "/work/agents/a1"},
		{"пустая песочница", "/srv/beacon.git", ""},
		{"пробельная песочница", "/srv/beacon.git", "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, out, errOut, calls := w68CLI(t, nil)

			if code := c.provisionSandbox(tc.source, tc.dest); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr пустой")
			}
			if len(*calls) != 0 {
				t.Fatalf("раннер не должен вызываться: %+v", *calls)
			}
		})
	}
}

// w132State points Beacon state at a fresh temporary directory.
func w132State(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
}

func TestW132AssignUniquePortsForFifteenAgents(t *testing.T) {
	r := newPortRegistry()
	seen := map[int]string{}
	for i := 0; i < portRegistryCount; i++ {
		id := fmt.Sprintf("agent-%d", i)
		port, err := r.assign(id)
		if err != nil {
			t.Fatalf("агент %s: %v", id, err)
		}
		if port < r.Base || port >= r.Base+r.Count {
			t.Fatalf("порт %d агента %s вне диапазона %d-%d", port, id, r.Base, r.Base+r.Count-1)
		}
		if prev, ok := seen[port]; ok {
			t.Fatalf("коллизия: %s и %s получили порт %d", prev, id, port)
		}
		seen[port] = id
	}
	if _, err := r.assign("agent-extra"); err == nil {
		t.Fatal("16-й агент получил порт, ожидалась ошибка исчерпания")
	}
}

func TestW132AssignIsIdempotent(t *testing.T) {
	r := newPortRegistry()
	p1, err := r.assign("task")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r.assign("task")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("повторная выдача: %d и %d", p1, p2)
	}
	p3, err := r.assign("other")
	if err != nil {
		t.Fatal(err)
	}
	if p3 == p1 {
		t.Fatalf("две задачи получили порт %d", p1)
	}
}

func TestW132AssignRejectsEmptyTask(t *testing.T) {
	r := newPortRegistry()
	for _, id := range []string{"", "  "} {
		if _, err := r.assign(id); err == nil {
			t.Fatalf("пустой ID %q принят", id)
		}
	}
}

func TestW132RegistrySurvivesRestart(t *testing.T) {
	w132State(t)
	reg, err := loadPortRegistry()
	if err != nil {
		t.Fatal(err)
	}
	port, err := reg.assign("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := savePortRegistry(reg); err != nil {
		t.Fatal(err)
	}

	// Новый процесс читает то же состояние из диска.
	reloaded, err := loadPortRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reloaded.Used["task-1"]; !ok || got != port {
		t.Fatalf("после рестарта порт задачи: %d, присутствие %v", got, ok)
	}
	other, err := reloaded.assign("task-2")
	if err != nil {
		t.Fatal(err)
	}
	if other == port {
		t.Fatalf("порт %d выдан повторно после рестарта", port)
	}
}

func TestW132LoadRejectsCollidingState(t *testing.T) {
	w132State(t)
	bad := &portRegistry{
		Base:  portRegistryBase,
		Count: portRegistryCount,
		Used:  map[string]int{"task-a": portRegistryBase, "task-b": portRegistryBase},
	}
	if err := savePortRegistry(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPortRegistry(); err == nil {
		t.Fatal("коллизия в состоянии не отклонена")
	}
}

func TestW132LoadRejectsOutOfRangePort(t *testing.T) {
	w132State(t)
	bad := &portRegistry{
		Base:  portRegistryBase,
		Count: portRegistryCount,
		Used:  map[string]int{"task": portRegistryBase + portRegistryCount},
	}
	if err := savePortRegistry(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPortRegistry(); err == nil {
		t.Fatal("порт вне диапазона не отклонён")
	}
}

func TestW132ReleaseKillsProcessesBeforeFreeingPort(t *testing.T) {
	r := newPortRegistry()
	port, err := r.assign("task")
	if err != nil {
		t.Fatal(err)
	}
	kill := func(got int) error {
		if got != port {
			t.Fatalf("снятие процессов на порту %d, ожидался %d", got, port)
		}
		// Пока процессы не сняты, порт ещё занят и не выдаётся другой задаче.
		other, err := r.assign("concurrent")
		if err != nil {
			t.Fatal(err)
		}
		if other == port {
			t.Fatalf("порт %d выдан до снятия процессов", port)
		}
		return nil
	}
	if err := r.release("task", kill); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Used["task"]; ok {
		t.Fatal("порт остался занятым после освобождения")
	}
	next, err := r.assign("next")
	if err != nil {
		t.Fatal(err)
	}
	if next != port {
		t.Fatalf("порт %d не выдан повторно после освобождения, получен %d", port, next)
	}
}

func TestW132ReleaseKeepsPortWhenKillFails(t *testing.T) {
	r := newPortRegistry()
	port, err := r.assign("task")
	if err != nil {
		t.Fatal(err)
	}
	wantErr := fmt.Errorf("не удалось снять процесс")
	called := false
	kill := func(int) error {
		called = true
		return wantErr
	}
	if err := r.release("task", kill); !errors.Is(err, wantErr) {
		t.Fatalf("ошибка %v, ожидалась %v", err, wantErr)
	}
	if !called {
		t.Fatal("сниматель не вызван")
	}
	if got, ok := r.Used["task"]; !ok || got != port {
		t.Fatalf("порт освобождён при ошибке снятия: %d, presence %v", got, ok)
	}
}

func TestW132ReleaseUnknownTaskDoesNotKill(t *testing.T) {
	r := newPortRegistry()
	called := false
	if err := r.release("missing", func(int) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("сниматель вызван для задачи без порта")
	}
}

func TestW132PortProcessKillerUsesRunner(t *testing.T) {
	c, _, _, calls := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 0}
	})
	if err := c.portProcessKiller(4123); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов раннера %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "fuser" {
		t.Fatalf("раннер вызвал %q, ожидался fuser", call.name)
	}
	if want := []string{"-k", "4123/tcp"}; !reflect.DeepEqual(call.args, want) {
		t.Fatalf("аргументы %q, ожидались %q", call.args, want)
	}
}

func TestW132PortProcessKillerNoListenerIsSuccess(t *testing.T) {
	c, _, _, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 1, stderr: []byte("no process found\n")}
	})
	if err := c.portProcessKiller(4123); err != nil {
		t.Fatalf("отсутствие слушателя должно считаться успехом: %v", err)
	}
}

func TestW132ReleasePortPersistsAfterKill(t *testing.T) {
	w132State(t)
	c, _, _, calls := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 0}
	})
	port, err := c.assignPort("task")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.releasePort("task", c.portProcessKiller); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("раннер вызван %d раз, ожидался 1", len(*calls))
	}
	reg, err := loadPortRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Used["task"]; ok {
		t.Fatal("задача осталась в реестре после освобождения")
	}
	next, err := c.assignPort("next")
	if err != nil {
		t.Fatal(err)
	}
	if next != port {
		t.Fatalf("порт %d не перевыдан после освобождения, получен %d", port, next)
	}
}

func TestW132ReleasePortDoesNotPersistOnKillFailure(t *testing.T) {
	w132State(t)
	c, _, _, _ := w68CLI(t, nil)
	port, err := c.assignPort("task")
	if err != nil {
		t.Fatal(err)
	}
	wantErr := fmt.Errorf("сбой снятия")
	if err := c.releasePort("task", func(int) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("ошибка %v, ожидалась %v", err, wantErr)
	}
	reloaded, err := loadPortRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reloaded.Used["task"]; !ok || got != port {
		t.Fatalf("порт потерян в состоянии при ошибке снятия: %d, presence %v", got, ok)
	}
}

// w168Stale replaces the read-only stale-task source and restores it after the
// test, like the other capability seams in this package.
func w168Stale(t *testing.T, fn func(time.Time) ([]staleTask, error)) {
	t.Helper()
	prev := readStaleTasks
	readStaleTasks = fn
	t.Cleanup(func() { readStaleTasks = prev })
}

func TestW168StaleColumnShowsTaskWithoutLiveLease(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	expired := time.Now().Add(-5 * time.Minute)
	w168Stale(t, func(time.Time) ([]staleTask, error) {
		return []staleTask{{TaskID: "T3.94", Agent: "reviewer", ExpiredAt: expired}}, nil
	})
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr не пустой: %q", errOut.String())
	}
	for _, want := range []string{"STALE", "T3.94", "5m"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("в таблице нет %q: %q", want, out.String())
		}
	}
}

func TestW168StaleAgeIsSinceLeaseExpiry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	expired := time.Now().Add(-2*time.Hour - 5*time.Minute)
	w168Stale(t, func(time.Time) ([]staleTask, error) {
		return []staleTask{{TaskID: "T3.94", Agent: "reviewer", ExpiredAt: expired}}, nil
	})
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "2h5m") {
		t.Fatalf("возраст от истечения аренды не показан: %q", out.String())
	}
}

func TestW168StaleJSONHasTaskAndAge(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	expired := time.Now().Add(-5 * time.Minute)
	w168Stale(t, func(time.Time) ([]staleTask, error) {
		return []staleTask{{TaskID: "T3.94", Agent: "reviewer", ExpiredAt: expired}}, nil
	})
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("строк %d, ожидалось 2", len(got))
	}
	var found bool
	for _, row := range got {
		if row.ID != "reviewer" {
			if len(row.Stale) != 0 {
				t.Fatalf("чужой STALE у %s: %+v", row.ID, row.Stale)
			}
			continue
		}
		found = true
		if len(row.Stale) != 1 {
			t.Fatalf("STALE агентов reviewer: %d, ожидался 1", len(row.Stale))
		}
		s := row.Stale[0]
		if s.TaskID != "T3.94" {
			t.Fatalf("задача в STALE: %q, ожидалась T3.94", s.TaskID)
		}
		if s.AgeSeconds < 295 || s.AgeSeconds > 310 {
			t.Fatalf("возраст %d с, ожидался около 300", s.AgeSeconds)
		}
	}
	if !found {
		t.Fatal("агент reviewer не найден в JSON")
	}
}

func TestW168StaleColumnOnlyReadsState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	tasks := []staleTask{{TaskID: "T3.94", Agent: "reviewer", ExpiredAt: time.Now().Add(-time.Minute)}}
	before := fmt.Sprintf("%+v", tasks)
	calls := 0
	w168Stale(t, func(time.Time) ([]staleTask, error) {
		calls++
		return tasks, nil
	})
	c, _, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if calls != 1 {
		t.Fatalf("источник аренд прочитан %d раз, ожидался 1", calls)
	}
	if got := fmt.Sprintf("%+v", tasks); got != before {
		t.Fatalf("снимок аренд изменён колонкой: было %s, стало %s", before, got)
	}
}

func TestW168NoStaleTasksShowsDashAndOmitsJSONField(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	w168Stale(t, func(time.Time) ([]staleTask, error) { return nil, nil })

	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})
	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "STALE") || !strings.Contains(out.String(), "-") {
		t.Fatalf("пустая колонка не показана: %q", out.String())
	}

	c, out, errOut, _ = w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})
	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "\"stale\"") {
		t.Fatalf("пустой STALE попал в JSON: %q", out.String())
	}
}

func TestW168StaleReadsSnapshotFromStateAndLeavesItUntouched(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", xdg)
	dir := filepath.Join(xdg, "beacon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)
	snapshot := fmt.Sprintf(`{"tasks":[{"taskID":"T3.94","agent":"reviewer","expiredAt":%q}]}`, expired)
	path := filepath.Join(dir, staleTasksFile)
	if err := os.WriteFile(path, []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	w68Service(t, []byte(w68Registration), nil)
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	for _, want := range []string{"STALE", "T3.94", "3m"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("в таблице нет %q: %q", want, out.String())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != snapshot {
		t.Fatalf("снимок аренд изменён: %q", data)
	}
}

func TestW168StaleTasksAreSortedByID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	w68Service(t, []byte(w68Registration), nil)
	now := time.Now()
	w168Stale(t, func(time.Time) ([]staleTask, error) {
		return []staleTask{
			{TaskID: "T3.99", Agent: "reviewer", ExpiredAt: now.Add(-time.Minute)},
			{TaskID: "T3.01", Agent: "reviewer", ExpiredAt: now.Add(-time.Minute)},
		}, nil
	})
	c, out, errOut, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})

	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var got []agentRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, row := range got {
		if row.ID != "reviewer" {
			continue
		}
		if len(row.Stale) != 2 || row.Stale[0].TaskID != "T3.01" || row.Stale[1].TaskID != "T3.99" {
			t.Fatalf("порядок STALE: %+v", row.Stale)
		}
		return
	}
	t.Fatal("агент reviewer не найден")
}

// ---- ADR 0058 п6 / T3.133: маршрутизация внимания и handoff между сессиями ----

// w133State points Beacon state at a fresh temporary directory and returns its
// beacon subdirectory.
func w133State(t *testing.T) string {
	t.Helper()
	xdg := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", xdg)
	dir := filepath.Join(xdg, "beacon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// w133Requests writes an attention-requests snapshot into Beacon state.
func w133Requests(t *testing.T, dir string, reqs []attentionRequest) {
	t.Helper()
	data, err := json.Marshal(attentionState{Requests: reqs})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, attentionRequestsFile)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// w133Session saves a session for a task, the same way attach does.
func w133Session(t *testing.T, taskID, sessionID string) {
	t.Helper()
	if err := rememberAttachSession(taskID, sessionID, time.Time{}); err != nil {
		t.Fatal(err)
	}
}

// w133AgentsCLI runs an agents command with a fixed app.agents response.
func w133AgentsCLI(t *testing.T) (*CLI, *bytes.Buffer, *bytes.Buffer, *[]w68Call) {
	t.Helper()
	w68Service(t, []byte(w68Registration), nil)
	return w68CLI(t, func(w68Call) runResult {
		return runResult{stdout: []byte(w68AgentList)}
	})
}

func TestW133RoutePicksAgentWithAttentionRequest(t *testing.T) {
	dir := w133State(t)
	w133Requests(t, dir, []attentionRequest{{
		Agent: "reviewer", TaskID: "T3.9", Reason: "нужен разбор", RequestedAt: time.Now().Add(-time.Minute),
	}})
	w133Session(t, "T3.9", "session-reviewer")
	c, out, errOut, _ := w133AgentsCLI(t)

	if code := c.runAgents([]string{"--route"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var got attentionRoute
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout не JSON: %q: %v", out.String(), err)
	}
	want := attentionRoute{
		Agent: "reviewer", Name: "Reviewer", TaskID: "T3.9",
		SessionID: "session-reviewer", Reason: "нужен разбор",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("маршрут %+v, ожидался %+v", got, want)
	}
}

func TestW133RouteWithoutRequestIsAnError(t *testing.T) {
	w133State(t)
	c, out, errOut, _ := w133AgentsCLI(t)

	if code := c.runAgents([]string{"--route"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "нет агента с запросом внимания") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
}

func TestW133RouteIgnoresRequestsForUnknownAgents(t *testing.T) {
	dir := w133State(t)
	w133Requests(t, dir, []attentionRequest{{Agent: "ghost", TaskID: "T3.9"}})
	c, out, errOut, _ := w133AgentsCLI(t)

	if code := c.runAgents([]string{"--route"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("неизвестный агент попал в маршрут: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "нет агента с запросом внимания") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
}

func TestW133RouteUsesEarliestRequestAndStableTieBreak(t *testing.T) {
	dir := w133State(t)
	now := time.Now()
	w133Requests(t, dir, []attentionRequest{
		{Agent: "general", TaskID: "T2", RequestedAt: now.Add(-time.Hour)},
		{Agent: "reviewer", TaskID: "T1", RequestedAt: now.Add(-2 * time.Hour)},
	})
	c, out, errOut, _ := w133AgentsCLI(t)
	if code := c.runAgents([]string{"--route"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var got attentionRoute
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Agent != "reviewer" {
		t.Fatalf("выбран %q, ожидался reviewer с более ранним запросом", got.Agent)
	}

	// При равном времени запросов побеждает меньший ID агента.
	w133Requests(t, dir, []attentionRequest{
		{Agent: "general", TaskID: "T2", RequestedAt: now},
		{Agent: "reviewer", TaskID: "T1", RequestedAt: now},
	})
	c, out, errOut, _ = w133AgentsCLI(t)
	if code := c.runAgents([]string{"--route"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Agent != "general" {
		t.Fatalf("выбран %q, ожидался general по tie-break", got.Agent)
	}
}

func TestW133AttentionMarkedInTableAndJSON(t *testing.T) {
	dir := w133State(t)
	w133Requests(t, dir, []attentionRequest{{Agent: "reviewer", TaskID: "T3.9"}})

	c, out, errOut, _ := w133AgentsCLI(t)
	if code := c.runAgents(nil); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "ATTENTION") {
		t.Fatalf("в таблице нет колонки ATTENTION: %q", out.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.HasPrefix(line, "reviewer") && !strings.HasSuffix(strings.TrimSpace(line), "+") {
			t.Fatalf("reviewer не помечен в таблице: %q", line)
		}
		if strings.HasPrefix(line, "general") && strings.HasSuffix(strings.TrimSpace(line), "+") {
			t.Fatalf("general помечен в таблице: %q", line)
		}
	}

	c, out, errOut, _ = w133AgentsCLI(t)
	if code := c.runAgents([]string{"--format", "json"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var rows []agentRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "reviewer" && !row.Attention {
			t.Fatalf("reviewer не помечен в JSON: %+v", row)
		}
		if row.ID == "general" && row.Attention {
			t.Fatalf("general помечен в JSON: %+v", row)
		}
	}
}

func TestW133HandoffRecordsTransferAndRoutingFollowsIt(t *testing.T) {
	dir := w133State(t)
	w133Session(t, "T3.9", "session-A")
	c, out, errOut, _ := w68CLI(t, nil)

	if code := c.runAgents([]string{"--handoff", "T3.9", "--to", "session-B"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var rec handoffRecord
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatalf("stdout не JSON: %q: %v", out.String(), err)
	}
	if rec.TaskID != "T3.9" || rec.From != "session-A" || rec.To != "session-B" {
		t.Fatalf("запись передачи %+v, ожидалась A -> B", rec)
	}

	w133Requests(t, dir, []attentionRequest{{Agent: "reviewer", TaskID: "T3.9"}})
	c, out, errOut, _ = w133AgentsCLI(t)
	if code := c.runAgents([]string{"--route"}); code != 0 {
		t.Fatalf("код выхода %d, ожидался 0; stderr %q", code, errOut.String())
	}
	var route attentionRoute
	if err := json.Unmarshal(out.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	if route.SessionID != "session-B" {
		t.Fatalf("маршрут ведёт в сессию %q, ожидалась session-B после передачи", route.SessionID)
	}
}

func TestW133HandoffIsIdempotent(t *testing.T) {
	dir := w133State(t)
	journal := func() handoffState {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, handoffsFile))
		if err != nil {
			t.Fatal(err)
		}
		var st handoffState
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}

	c, out, _, _ := w68CLI(t, nil)
	if code := c.runAgents([]string{"--handoff", "T3.9", "--to", "session-B"}); code != 0 {
		t.Fatalf("первый вызов: код %d", code)
	}
	first := out.String()
	c, out, _, _ = w68CLI(t, nil)
	if code := c.runAgents([]string{"--handoff", "T3.9", "--to", "session-B"}); code != 0 {
		t.Fatalf("повторный вызов: код %d", code)
	}
	if out.String() != first {
		t.Fatalf("повтор изменил запись: %q против %q", out.String(), first)
	}
	if st := journal(); len(st.Handoffs) != 1 {
		t.Fatalf("передач %d, ожидалась 1", len(st.Handoffs))
	}

	c, _, _, _ = w68CLI(t, nil)
	if code := c.runAgents([]string{"--handoff", "T3.9", "--to", "session-C"}); code != 0 {
		t.Fatalf("передача в другую сессию: код %d", code)
	}
	if st := journal(); len(st.Handoffs) != 2 {
		t.Fatalf("передач %d, ожидалось 2", len(st.Handoffs))
	}
}

func TestW133HandoffRejectsMissingArgs(t *testing.T) {
	w133State(t)
	for _, args := range [][]string{
		{"--handoff", "T3.9"},
		{"--to", "session-B"},
		{"--handoff", "  ", "--to", "session-B"},
		{"--handoff", "T3.9", "--to", "  "},
		{"--handoff", "T3.9", "--to", "session-B", "--route"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, out, errOut, _ := w68CLI(t, nil)
			if code := c.runAgents(args); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr пустой")
			}
		})
	}
}

func TestW133RouteReportsMalformedAttentionState(t *testing.T) {
	dir := w133State(t)
	if err := os.WriteFile(filepath.Join(dir, attentionRequestsFile), []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, out, errOut, _ := w133AgentsCLI(t)
	if code := c.runAgents([]string{"--route"}); code != 2 {
		t.Fatalf("код выхода %d, ожидался 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout не пустой: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "запросы внимания") {
		t.Fatalf("нет понятной ошибки: %q", errOut.String())
	}
}

// ---- ADR 0063 фича 6 / T3.158: мультирепо-задача с отдельным worktree ----

func w158Bindings(t *testing.T, dir string) repoWorktreeState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, repoWorktreesFile))
	if err != nil {
		t.Fatal(err)
	}
	var st repoWorktreeState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestW158BindRepoWorktreeProvisionsGitWorktree(t *testing.T) {
	dir := w133State(t)
	c, out, errOut, calls := w68CLI(t, func(w68Call) runResult { return runResult{} })
	wt := filepath.Join(t.TempDir(), "wt-A")

	b, err := c.bindRepoWorktree("T3.158", "/srv/repoA.git", wt)
	if err != nil {
		t.Fatal(err)
	}
	if b.TaskID != "T3.158" || b.Repo != "/srv/repoA.git" || b.Worktree != wt {
		t.Fatalf("связка %+v, ожидалась T3.158/repoA/%s", b, wt)
	}
	if b.Branch == "" {
		t.Fatal("ветка не выделена")
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("неожиданный вывод: out=%q err=%q", out.String(), errOut.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("вызовов раннера %d, ожидался 1", len(*calls))
	}
	call := (*calls)[0]
	if call.name != "git" || call.dir != "/srv/repoA.git" {
		t.Fatalf("раннер вызвал %q в %q, ожидался git в репозитории", call.name, call.dir)
	}
	want := []string{"worktree", "add", "-b", b.Branch, wt}
	if !reflect.DeepEqual(call.args, want) {
		t.Fatalf("аргументы %q, ожидались %q", call.args, want)
	}
	if st := w158Bindings(t, dir); len(st.Bindings) != 1 {
		t.Fatalf("в состоянии %d связок, ожидалась 1", len(st.Bindings))
	}
}

func TestW158TaskCanBindSeveralReposWithDistinctWorktrees(t *testing.T) {
	w133State(t)
	c, _, _, calls := w68CLI(t, func(w68Call) runResult { return runResult{} })
	base := t.TempDir()

	b1, err := c.bindRepoWorktree("T3.158", "/srv/repoA.git", filepath.Join(base, "a"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := c.bindRepoWorktree("T3.158", "/srv/repoB.git", filepath.Join(base, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if b1.Worktree == b2.Worktree {
		t.Fatalf("два репозитория получили один worktree %q", b1.Worktree)
	}
	if b1.Branch == b2.Branch {
		t.Fatalf("два репозитория получили одну ветку %q", b1.Branch)
	}
	if len(*calls) != 2 {
		t.Fatalf("вызовов git %d, ожидалось 2", len(*calls))
	}
}

func TestW158BindIsIdempotent(t *testing.T) {
	w133State(t)
	c, _, _, calls := w68CLI(t, func(w68Call) runResult { return runResult{} })
	wt := filepath.Join(t.TempDir(), "wt")

	b1, err := c.bindRepoWorktree("T3.158", "/srv/repoA.git", wt)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := c.bindRepoWorktree("T3.158", "/srv/repoA.git", filepath.Join(t.TempDir(), "ignored"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b1, b2) {
		t.Fatalf("повтор изменил связку: %+v против %+v", b1, b2)
	}
	if len(*calls) != 1 {
		t.Fatalf("повтор вызвал git %d раз, ожидался 1", len(*calls))
	}
}

func TestW158SameRepoDifferentTasksGetDistinctWorktrees(t *testing.T) {
	w133State(t)
	c, _, _, _ := w68CLI(t, func(w68Call) runResult { return runResult{} })
	base := t.TempDir()

	b1, err := c.bindRepoWorktree("T1", "/srv/repo.git", filepath.Join(base, "t1"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := c.bindRepoWorktree("T2", "/srv/repo.git", filepath.Join(base, "t2"))
	if err != nil {
		t.Fatal(err)
	}
	if b1.Worktree == b2.Worktree || b1.Branch == b2.Branch {
		t.Fatalf("две задачи получили общий worktree/ветку: %+v и %+v", b1, b2)
	}
}

func TestW158BindRejectsEmptyInput(t *testing.T) {
	w133State(t)
	for _, tc := range []struct {
		name string
		task string
		repo string
	}{
		{"пустая задача", "", "/srv/repo.git"},
		{"пробельная задача", "  ", "/srv/repo.git"},
		{"пустой репозиторий", "T1", ""},
		{"пробельный репозиторий", "T1", "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _, calls := w68CLI(t, nil)
			if _, err := c.bindRepoWorktree(tc.task, tc.repo, "/tmp/wt"); err == nil {
				t.Fatal("пустой ввод принят")
			}
			if len(*calls) != 0 {
				t.Fatalf("раннер вызван при пустом вводе: %+v", *calls)
			}
		})
	}
}

func TestW158BindGitFailureDoesNotPersist(t *testing.T) {
	dir := w133State(t)
	c, _, _, _ := w68CLI(t, func(w68Call) runResult {
		return runResult{code: 128, stderr: []byte("fatal: worktree already exists\n")}
	})

	if _, err := c.bindRepoWorktree("T1", "/srv/repo.git", "/tmp/wt"); err == nil {
		t.Fatal("ошибка git проигнорирована")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("в ошибке нет причины: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, repoWorktreesFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("связка записана после ошибки git: %v", err)
	}
}

func TestW158BindingSurvivesRestart(t *testing.T) {
	dir := w133State(t)
	respond := func(w68Call) runResult { return runResult{} }
	c, _, _, _ := w68CLI(t, respond)
	first, err := c.bindRepoWorktree("T1", "/srv/repoA.git", filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatal(err)
	}
	if st := w158Bindings(t, dir); len(st.Bindings) != 1 {
		t.Fatalf("связок %d, ожидалась 1", len(st.Bindings))
	}

	// Новый процесс читает то же состояние и не выделяет второй worktree.
	c2, _, _, calls := w68CLI(t, respond)
	again, err := c2.bindRepoWorktree("T1", "/srv/repoA.git", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.Worktree != first.Worktree || again.Branch != first.Branch {
		t.Fatalf("после рестарта связка %+v, ожидалась %+v", again, first)
	}
	if len(*calls) != 0 {
		t.Fatalf("после рестарта git вызван %d раз, ожидался 0", len(*calls))
	}
}

func TestW158RejectsWorktreeSharedByTwoBindings(t *testing.T) {
	w133State(t)
	c, _, _, _ := w68CLI(t, func(w68Call) runResult { return runResult{} })
	wt := filepath.Join(t.TempDir(), "shared")

	if _, err := c.bindRepoWorktree("T1", "/srv/repoA.git", wt); err != nil {
		t.Fatal(err)
	}
	if _, err := c.bindRepoWorktree("T2", "/srv/repoB.git", wt); err == nil {
		t.Fatal("второй задаче отдан чужой worktree")
	}
}

func TestW158DefaultWorktreePathIsUnderState(t *testing.T) {
	dir := w133State(t)
	c, _, _, _ := w68CLI(t, func(w68Call) runResult { return runResult{} })

	b, err := c.bindRepoWorktree("T1", "/srv/repo.git", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, repoWorktreesDir, repoWorktreeKey("T1", "/srv/repo.git"))
	if b.Worktree != want {
		t.Fatalf("путь worktree %q, ожидался %q", b.Worktree, want)
	}
	if !strings.HasPrefix(b.Branch, "beacon/") {
		t.Fatalf("ветка %q не пространстве beacon/", b.Branch)
	}
}

func TestW158ReviewGateBeforePR(t *testing.T) {
	w133State(t)
	c, _, _, _ := w68CLI(t, func(w68Call) runResult { return runResult{} })
	if _, err := c.bindRepoWorktree("T1", "/srv/repo.git", "/tmp/wt"); err != nil {
		t.Fatal(err)
	}

	if _, err := recordRepoPR("T1", "/srv/repo.git", 42); err == nil {
		t.Fatal("PR записан до review-гейта")
	}
	reviewed, err := markRepoReviewed("T1", "/srv/repo.git")
	if err != nil || !reviewed.Reviewed {
		t.Fatalf("review-гейт не пройден: %+v, %v", reviewed, err)
	}
	pr, err := recordRepoPR("T1", "/srv/repo.git", 42)
	if err != nil || pr.PR != 42 {
		t.Fatalf("PR не записан: %+v, %v", pr, err)
	}
	if _, err := markRepoReviewed("нет", "/srv/repo.git"); err == nil {
		t.Fatal("review-гейт применён к неизвестной связке")
	}
}

func TestW158RunAgentsBindsRepoAndPrintsJSON(t *testing.T) {
	w133State(t)
	wt := filepath.Join(t.TempDir(), "wt")
	c, out, errOut, calls := w68CLI(t, func(w68Call) runResult { return runResult{} })

	code := c.runAgents([]string{"--task", "T3.158", "--repo", "/srv/repo.git", "--worktree", wt})
	if code != 0 {
		t.Fatalf("код выхода %d, stderr %q", code, errOut.String())
	}
	var got repoWorktree
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout не JSON: %q: %v", out.String(), err)
	}
	if got.TaskID != "T3.158" || got.Worktree != wt || got.Branch == "" {
		t.Fatalf("связка в JSON: %+v", got)
	}
	if len(*calls) != 1 || (*calls)[0].name != "git" {
		t.Fatalf("git не вызван через раннер: %+v", *calls)
	}
}

func TestW158RunAgentsBindRejectsMissingArgs(t *testing.T) {
	w133State(t)
	for _, args := range [][]string{
		{"--task", "T3.158"},
		{"--repo", "/srv/repo.git"},
		{"--worktree", "/tmp/wt"},
		{"--task", "T3.158", "--repo", "/srv/repo.git", "--route"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, out, errOut, _ := w68CLI(t, nil)
			if code := c.runAgents(args); code != 2 {
				t.Fatalf("код выхода %d, ожидался 2", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout не пустой: %q", out.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("stderr пустой")
			}
		})
	}
}
