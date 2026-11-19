package taskcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

type env struct {
	svc      *Service
	cfg      Config
	tasksDir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	tasksDir := filepath.Join(root, "docs", "tasks")
	adrDir := filepath.Join(root, "docs", "adrs")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{tasksDir, adrDir, stateDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, status := range map[string]string{"0001-accepted.md": "accepted", "0002-default.md": "default", "0003-open.md": "open"} {
		if err := os.WriteFile(filepath.Join(adrDir, name), []byte("Статус: "+status+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{TasksDir: tasksDir, StateDir: stateDir, ADRDir: adrDir, Root: root}
	return &env{svc: NewService(cfg), cfg: cfg, tasksDir: tasksDir}
}

func (e *env) card(t *testing.T, id, status, typ, ref, owners, agent, when string) {
	t.Helper()
	text := fmt.Sprintf("# %s. Тест\nСтатус: %s\nФаза: P2\nТип: %s\n"+
		"Владелец файлов: %s\nЗависимости: нет\nСсылка: %s\nАгент: %s\nВремя: %s\n"+
		"Цель: ц\nКонтекст: к\nШаги: ш\nПроверка: п\nПриёмка: пр\n",
		id, status, typ, owners, ref, agent, when)
	if err := os.WriteFile(filepath.Join(e.tasksDir, id+".md"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) good() NewOptions {
	return NewOptions{
		Type: "task", Phase: "P2", Title: "Новая задача", Reference: "ADR 0001",
		OwnerFiles: "internal/app/fresh/one.go",
	}
}

func TestListJSONShapeFiltersAndViolation(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.4", "todo", "task", "ADR 0001", "internal/infra/mcp/server.go", "нет", "нет")
	e.card(t, "T1.2", "done", "task", "ADR 0001", "internal/app/other/one.go", "agent-1", "2026-11-19T14:03:21Z")

	out, err := e.svc.List(ListOptions{Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("неверный JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0]["id"] != "T1.2" || rows[1]["id"] != "T2.4" {
		t.Fatalf("порядок или число карточек: %s", out)
	}
	fields := []string{"id", "type", "status", "phase", "ownerFiles", "dependencies", "reference", "agent", "time", "title"}
	if len(rows[0]) != len(fields) {
		t.Fatalf("полей %d, ждали %d: %s", len(rows[0]), len(fields), out)
	}
	for _, f := range fields {
		if _, ok := rows[0][f]; !ok {
			t.Errorf("нет поля %s", f)
		}
	}
	deps, ok := rows[0]["dependencies"].([]any)
	if !ok || len(deps) != 0 {
		t.Errorf("dependencies должен быть [], получил %v", rows[0]["dependencies"])
	}

	// OR внутри фильтра, AND между фильтрами.
	out, err = e.svc.List(ListOptions{Format: "json", Statuses: []string{"todo", "done"}, Phases: []string{"P2"}})
	if err != nil || !strings.Contains(out, "T2.4") || !strings.Contains(out, "T1.2") {
		t.Fatalf("фильтр OR: %v\n%s", err, out)
	}
	out, err = e.svc.List(ListOptions{Format: "json", Statuses: []string{"todo"}, Agents: []string{"agent-1"}})
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("фильтр AND должен дать [], получил %v\n%s", err, out)
	}
	out, err = e.svc.List(ListOptions{Format: "json", Files: []string{"internal/infra/mcp"}})
	if err != nil || !strings.Contains(out, "T2.4") || strings.Contains(out, "T1.2") {
		t.Fatalf("фильтр --file: %v\n%s", err, out)
	}

	// Посаженное нарушение: карточка без Статуса.
	bad := filepath.Join(e.tasksDir, "T3.1.md")
	good, _ := os.ReadFile(filepath.Join(e.tasksDir, "T2.4.md"))
	if err := os.WriteFile(bad, []byte(strings.Replace(string(good), "Статус: todo\n", "", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	out, err = e.svc.List(ListOptions{Format: "json"})
	if taskstore.Code(err) != 1 || out != "" || !strings.Contains(err.Error(), "T3.1.md") {
		t.Fatalf("нарушение не найдено: out=%q err=%v", out, err)
	}
}

func TestListTableAndBadFormat(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.1", "todo", "task", "ADR 0001", "internal/app/one.go", "нет", "нет")
	out, err := e.svc.List(ListOptions{})
	if err != nil || !strings.Contains(out, "ID") || !strings.Contains(out, "T2.1") {
		t.Fatalf("таблица: %v\n%s", err, out)
	}
	if _, err := e.svc.List(ListOptions{Format: "yaml"}); taskstore.Code(err) != 1 {
		t.Fatalf("неизвестный формат должен дать код 1, получил %v", err)
	}
	if _, err := e.svc.List(ListOptions{Statuses: []string{"open"}}); taskstore.Code(err) != 1 {
		t.Fatalf("неизвестный статус должен дать код 1, получил %v", err)
	}
}

func TestNewDryRunAndCreate(t *testing.T) {
	e := newEnv(t)
	o := e.good()
	o.DryRun = true
	id, p, err := e.svc.New(o)
	if err != nil || id != "T2.1" || p != filepath.Join(e.tasksDir, "T2.1.md") {
		t.Fatalf("dry-run: %s %s %v", id, p, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("dry-run создал файл: %v", err)
	}
	if _, err := os.Stat(taskstore.CounterPath(e.cfg.StateDir, e.cfg.Root)); !os.IsNotExist(err) {
		t.Fatalf("dry-run изменил счётчик: %v", err)
	}
	o.DryRun = false
	id, _, err = e.svc.New(o)
	if err != nil || id != "T2.1" {
		t.Fatalf("new: %s %v", id, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# T2.1. Новая задача\n", "Статус: todo", "Фаза: P2", "Тип: task",
		"Владелец файлов: internal/app/fresh/one.go", "Зависимости: нет",
		"Ссылка: ADR 0001", "Агент: нет", "Время: нет", "Цель: заполнить при работе",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("в карточке нет %q:\n%s", want, data)
		}
	}
	lockPath := taskstore.LockPath(e.cfg.StateDir, e.cfg.Root)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("замок не отпущен: %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e *env, o *NewOptions)
	}{
		{"нет ADR", func(t *testing.T, e *env, o *NewOptions) { o.Reference = "ADR 0099" }},
		{"нет эпика", func(t *testing.T, e *env, o *NewOptions) { o.Reference = "T9.9" }},
		{"ADR open", func(t *testing.T, e *env, o *NewOptions) { o.Reference = "ADR 0003" }},
		{"эпик со ссылкой на задачу", func(t *testing.T, e *env, o *NewOptions) {
			o.Type = "epic"
			o.Reference = "T2.1"
		}},
		{"занятый файл", func(t *testing.T, e *env, o *NewOptions) {
			e.card(t, "T2.1", "todo", "task", "ADR 0001", "internal/app/fresh/one.go", "нет", "нет")
		}},
		{"файл внутри каталога in_progress", func(t *testing.T, e *env, o *NewOptions) {
			e.card(t, "T2.1", "in_progress", "task", "ADR 0001", "internal/app/fresh/", "agent-1", "2026-11-19T14:03:21Z")
		}},
		{"нет фазы", func(t *testing.T, e *env, o *NewOptions) { o.Phase = "" }},
		{"неизвестный флаг значения", func(t *testing.T, e *env, o *NewOptions) { o.Type = "bug" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.good()
			tc.setup(t, e, &o)
			before, _ := os.ReadDir(e.tasksDir)
			_, _, err := e.svc.New(o)
			if taskstore.Code(err) != 1 {
				t.Fatalf("код = %d, ждали 1, err=%v", taskstore.Code(err), err)
			}
			after, _ := os.ReadDir(e.tasksDir)
			if len(before) != len(after) {
				t.Fatalf("создан файл при провале валидации")
			}
			if strings.Contains(tc.name, "занят") || strings.Contains(tc.name, "каталога") {
				if !strings.Contains(err.Error(), "T2.1") {
					t.Errorf("в ошибке нет ID карточки-владельца: %v", err)
				}
			}
		})
	}
}

func TestNewDoneAndDroppedFreeFiles(t *testing.T) {
	for _, status := range []string{"done", "dropped"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			e.card(t, "T2.1", status, "task", "ADR 0001", "internal/app/fresh/one.go", "agent-1", "2026-11-19T14:03:21Z")
			if _, _, err := e.svc.New(e.good()); err != nil {
				t.Fatalf("статус %s должен освобождать файлы: %v", status, err)
			}
		})
	}
}

func TestNewIDAfterDeletion(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= 4; i++ {
		o := e.good()
		o.OwnerFiles = fmt.Sprintf("internal/app/fresh/%d.go", i)
		if _, _, err := e.svc.New(o); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(e.tasksDir, "T2.4.md")); err != nil {
		t.Fatal(err)
	}
	o := e.good()
	o.OwnerFiles = "internal/app/fresh/5.go"
	id, _, err := e.svc.New(o)
	if err != nil || id != "T2.5" {
		t.Fatalf("после удаления ID = %s, err=%v, ждали T2.5", id, err)
	}
}

func TestNewRace20(t *testing.T) {
	for rep := 0; rep < 20; rep++ {
		e := newEnv(t)
		// Разные владельцы: одинаковые пути запрещены правилом 4.6.
		o1, o2 := e.good(), e.good()
		o1.OwnerFiles = fmt.Sprintf("internal/app/fresh/a%d.go", rep)
		o2.OwnerFiles = fmt.Sprintf("internal/app/fresh/b%d.go", rep)
		ids := make(chan string, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, o := range []NewOptions{o1, o2} {
			wg.Add(1)
			go func(o NewOptions) {
				defer wg.Done()
				id, _, err := e.svc.New(o)
				ids <- id
				errs <- err
			}(o)
		}
		wg.Wait()
		close(ids)
		close(errs)
		var got []string
		for err := range errs {
			if err != nil {
				t.Fatalf("повтор %d: new дал ошибку %v", rep, err)
			}
		}
		for id := range ids {
			got = append(got, id)
		}
		if len(got) != 2 || got[0] == got[1] {
			t.Fatalf("повтор %d: одинаковые ID %v", rep, got)
		}
		entries, _ := os.ReadDir(e.tasksDir)
		if len(entries) != 2 {
			t.Fatalf("повтор %d: создано %d файлов, ждали 2", rep, len(entries))
		}
	}
}

func TestTakeChangesStatusAndSecondTakeFails(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.1", "todo", "task", "ADR 0001", "internal/app/one.go", "нет", "нет")
	out, err := e.svc.Take(TakeOptions{ID: "T2.1", Agent: "agent-1", Time: "2026-11-19T14:03:21Z"})
	if err != nil || out != "T2.1 in_progress agent-1 2026-11-19T14:03:21Z" {
		t.Fatalf("take: %q %v", out, err)
	}
	data, _ := os.ReadFile(filepath.Join(e.tasksDir, "T2.1.md"))
	card, err := taskstore.ParseCard("T2.1.md", data)
	if err != nil || card.Status != "in_progress" || card.Agent != "agent-1" || card.Time != "2026-11-19T14:03:21Z" {
		t.Fatalf("карточка после take: %+v %v", card, err)
	}
	if !strings.Contains(string(data), "# T2.1. Тест") || !strings.Contains(string(data), "Ссылка: ADR 0001") {
		t.Fatalf("take изменил лишние строки:\n%s", data)
	}
	_, err = e.svc.Take(TakeOptions{ID: "T2.1.md", Agent: "agent-2", Time: "2026-11-19T15:00:00Z"})
	if taskstore.Code(err) != 1 || !strings.Contains(err.Error(), "agent-1") || !strings.Contains(err.Error(), "2026-11-19T14:03:21Z") {
		t.Fatalf("второй take должен дать код 1 с агентом и временем: %v", err)
	}
	lockPath := taskstore.LockPath(e.cfg.StateDir, e.cfg.Root)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("замок не отпущен: %v", err)
	}
}

func TestTakeBadInput(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.1", "done", "task", "ADR 0001", "internal/app/one.go", "нет", "нет")
	for _, o := range []TakeOptions{
		{ID: "T2.1", Agent: "agent-1"},
		{ID: "T2.1", Agent: "agent-1", Time: "не время"},
		{ID: "T2.1"},
		{ID: "bad", Agent: "agent-1"},
		{ID: "T9.9", Agent: "agent-1"},
	} {
		if _, err := e.svc.Take(o); taskstore.Code(err) != 1 {
			t.Errorf("Take(%+v) дал %v, ждали код 1", o, err)
		}
	}
}

func TestTakeRace20(t *testing.T) {
	for rep := 0; rep < 20; rep++ {
		e := newEnv(t)
		id := fmt.Sprintf("T4.%d", rep+1)
		e.card(t, id, "todo", "task", "ADR 0001", fmt.Sprintf("internal/app/race%d.go", rep), "нет", "нет")
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for n := 1; n <= 2; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				_, err := e.svc.Take(TakeOptions{ID: id, Agent: fmt.Sprintf("agent-%d", n), Time: "2026-11-19T14:03:21Z"})
				codes <- taskstore.Code(err)
			}(n)
		}
		wg.Wait()
		close(codes)
		var got []int
		for c := range codes {
			got = append(got, c)
		}
		if !((got[0] == 0 && got[1] == 1) || (got[0] == 1 && got[1] == 0)) {
			t.Fatalf("повтор %d: коды %v, ждали 0 и 1", rep, got)
		}
		data, _ := os.ReadFile(filepath.Join(e.tasksDir, id+".md"))
		card, err := taskstore.ParseCard(id+".md", data)
		if err != nil || card.Status != "in_progress" || card.Agent == "нет" || card.Time == "нет" {
			t.Fatalf("повтор %d: карточка после гонки %+v, %v", rep, card, err)
		}
		if strings.Count(string(data), "Агент: ") != 1 || strings.Count(string(data), "Время: ") != 1 {
			t.Fatalf("повтор %d: в карточке несколько агентов или времён:\n%s", rep, data)
		}
	}
}

func TestSelfTest(t *testing.T) {
	var out strings.Builder
	if err := SelfTest(&out); err != nil {
		t.Fatalf("SelfTest: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "self-test passed") {
		t.Fatalf("нет итоговой строки: %s", out.String())
	}
}
