package taskcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
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

func TestListUsesTaskstoreStatusContract(t *testing.T) {
	e := newEnv(t)
	statuses := taskstore.ValidStatuses()
	for i, status := range statuses {
		id := fmt.Sprintf("T2.%d", i+1)
		e.card(t, id, status, "task", "ADR 0001", fmt.Sprintf("internal/app/%d.go", i+1), "нет", "нет")
	}

	for _, status := range statuses {
		out, err := e.svc.List(ListOptions{Format: "json", Statuses: []string{status}})
		if err != nil {
			t.Errorf("статус %s из taskstore.ValidStatuses отвергнут: %v", status, err)
			continue
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0]["status"] != status {
			t.Errorf("фильтр %s: %s", status, out)
		}
	}
	if _, err := e.svc.List(ListOptions{Statuses: []string{"closed"}}); taskstore.Code(err) != 1 {
		t.Fatalf("неизвестный статус должен дать код 1, получил %v", err)
	}
}

func TestNewUsesTaskstoreIDContract(t *testing.T) {
	for _, id := range []string{"ATX-R01", "RS-CMP-06", "T3.17", "WIRING-485", "S1.001"} {
		if !taskstore.IsID(id) {
			t.Fatalf("тестовая форма %s не проходит taskstore.IsID", id)
		}
		t.Run("зависимость "+id, func(t *testing.T) {
			e := newEnv(t)
			o := e.good()
			o.Dependencies = id + ", T2.9"
			idNew, _, err := e.svc.New(o)
			if err != nil {
				t.Fatalf("зависимость %s отвергнута: %v", id, err)
			}
			data, err := os.ReadFile(filepath.Join(e.tasksDir, idNew+".md"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "Зависимости: "+id+", T2.9") {
				t.Errorf("зависимость не сохранена: %s", data)
			}
		})
		t.Run("ссылка "+id, func(t *testing.T) {
			e := newEnv(t)
			e.card(t, id, "todo", "epic", "ADR 0001", "internal/app/epic.go", "нет", "нет")
			o := e.good()
			o.Reference = id
			if _, _, err := e.svc.New(o); err != nil {
				t.Fatalf("ссылка на эпик %s отвергнута: %v", id, err)
			}
		})
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

// httpStatusError имитирует внешнего клиента, который сообщает 429 своим типом.
// Проверяет, что правило маршрутизации не привязано к конкретному пакету.
type httpStatusError struct {
	code int
	msg  string
}

func (e httpStatusError) Error() string { return e.msg }
func (e httpStatusError) Status() int   { return e.code }

func TestFallbackChainOrder(t *testing.T) {
	want := []string{"claude-code", "hermes", "gemini"}
	if len(FallbackChain) != len(want) {
		t.Fatalf("цепочка из %d звеньев, ждали %d", len(FallbackChain), len(want))
	}
	for i := range want {
		if FallbackChain[i] != want[i] {
			t.Errorf("звено %d = %q, ждали %q", i, FallbackChain[i], want[i])
		}
	}
}

func TestIsRateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"нет ошибки", nil, false},
		{"RateLimited", &RateLimited{}, true},
		{"завернутый RateLimited", fmt.Errorf("upstream: %w", &RateLimited{}), true},
		{"чужой 429", httpStatusError{code: 429, msg: "quota"}, true},
		{"чужой 500", httpStatusError{code: 500, msg: "boom"}, false},
		{"обычная ошибка", fmt.Errorf("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRateLimit(tc.err); got != tc.want {
				t.Errorf("IsRateLimit() = %v, ждали %v", got, tc.want)
			}
		})
	}
}

func TestJobFallbackKeepsExternalID(t *testing.T) {
	job, err := NewJob("T3.22")
	if err != nil || job.ID != "T3.22" || job.Executor != ExecutorClaudeCode {
		t.Fatalf("NewJob: %+v %v", job, err)
	}
	steps := []struct {
		from, to string
		ok       bool
	}{
		{ExecutorClaudeCode, ExecutorHermes, true},
		{ExecutorHermes, ExecutorGemini, true},
		{ExecutorGemini, ExecutorGemini, false},
	}
	for i, step := range steps {
		if job.Executor != step.from {
			t.Fatalf("шаг %d: исполнитель %q, ждали %q", i, job.Executor, step.from)
		}
		next, ok := NextOnRateLimit(job)
		if ok != step.ok {
			t.Fatalf("шаг %d: ok = %v, ждали %v", i, ok, step.ok)
		}
		if next.ID != "T3.22" {
			t.Errorf("шаг %d: внешний ID изменился на %q", i, next.ID)
		}
		if next.Executor != step.to {
			t.Errorf("шаг %d: исполнитель %q, ждали %q", i, next.Executor, step.to)
		}
		job = next
	}
	if len(job.Tried) != 2 || job.Tried[0] != ExecutorClaudeCode || job.Tried[1] != ExecutorHermes {
		t.Errorf("Tried = %v, ждали [claude-code hermes]", job.Tried)
	}
}

func TestNewJobRejectsEmptyID(t *testing.T) {
	if _, err := NewJob("  "); taskstore.Code(err) != 1 {
		t.Errorf("NewJob с пустым ID дал %v, ждали код 1", err)
	}
}

func TestRouteRateLimitWalksChainAndKeepsCardID(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.22", "in_progress", "task", "ADR 0001", "internal/app/x.go",
		ExecutorClaudeCode, "2026-11-19T14:03:21Z")
	path := filepath.Join(e.tasksDir, "T3.22.md")

	fromExec := []string{ExecutorClaudeCode, ExecutorHermes, ExecutorGemini}
	wantExec := []string{ExecutorHermes, ExecutorGemini, ExecutorGemini}
	for i, want := range wantExec {
		res, err := e.svc.RouteRateLimit("T3.22", &RateLimited{Executor: fromExec[i]})
		if err != nil {
			t.Fatalf("шаг %d: RouteRateLimit: %v", i, err)
		}
		if res.JobID != "T3.22" {
			t.Errorf("шаг %d: внешний ID = %q, ждали T3.22", i, res.JobID)
		}
		if res.To != want || res.Status != "in_progress "+want {
			t.Errorf("шаг %d: результат %+v, ждали исполнителя %q", i, res, want)
		}
		if res.Exhausted != (i == 2) {
			t.Errorf("шаг %d: Exhausted = %v", i, res.Exhausted)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		card, err := taskstore.ParseCard("T3.22.md", data)
		if err != nil {
			t.Fatalf("шаг %d: карточка не читается: %v", i, err)
		}
		if card.ID != "T3.22" {
			t.Errorf("шаг %d: ID карточки = %q", i, card.ID)
		}
		if card.Agent != want {
			t.Errorf("шаг %d: Агент = %q, ждали %q", i, card.Agent, want)
		}
		if !strings.Contains(string(data), "Исполнитель: "+want) {
			t.Errorf("шаг %d: в статусе нет исполнителя %q:\n%s", i, want, data)
		}
		if !strings.Contains(string(data), "Фолбэк:") {
			t.Errorf("шаг %d: в статусе нет записи о фолбэке:\n%s", i, data)
		}
		if strings.Count(string(data), "Исполнитель:") != 1 || strings.Count(string(data), "Фолбэк:") != 1 {
			t.Errorf("шаг %d: поля продублировались:\n%s", i, data)
		}
	}
}

func TestRouteRateLimitStartsFromFirstRungForForeignAgent(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.22", "todo", "task", "ADR 0001", "internal/app/x.go", "нет", "нет")
	res, err := e.svc.RouteRateLimit("T3.22.md", &RateLimited{})
	if err != nil || res.From != ExecutorClaudeCode || res.To != ExecutorHermes {
		t.Fatalf("фолбэк с чужим агентом: %+v %v", res, err)
	}
}

func TestRouteRateLimitRejectsNon429AndBadInput(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.22", "in_progress", "task", "ADR 0001", "internal/app/x.go",
		ExecutorClaudeCode, "2026-11-19T14:03:21Z")
	path := filepath.Join(e.tasksDir, "T3.22.md")
	before, _ := os.ReadFile(path)

	_, err := e.svc.RouteRateLimit("T3.22", fmt.Errorf("boom"))
	if taskstore.Code(err) != 1 {
		t.Errorf("не-429 дал %v, ждали код 1", err)
	}
	for _, id := range []string{"", "bad", "T9.9"} {
		if _, err := e.svc.RouteRateLimit(id, &RateLimited{}); taskstore.Code(err) != 1 {
			t.Errorf("ID %q дал %v, ждали код 1", id, err)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("карточка изменилась при отклонённом фолбэке:\n%s", after)
	}
	if _, err := os.Stat(taskstore.LockPath(e.cfg.StateDir, e.cfg.Root)); !os.IsNotExist(err) {
		t.Errorf("замок не отпущен: %v", err)
	}
}

func TestRouteRateLimitRejectsClosedCard(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.22", "done", "task", "ADR 0001", "internal/app/x.go", ExecutorClaudeCode, "2026-11-19T14:03:21Z")
	if _, err := e.svc.RouteRateLimit("T3.22", &RateLimited{}); taskstore.Code(err) != 1 {
		t.Errorf("закрытая карточка дала %v, ждали код 1", err)
	}
}

func TestBuildHandoffFromCard(t *testing.T) {
	e := newEnv(t)
	text := "# T3.25. Structured handoff\nСтатус: in_progress\nФаза: P3\nТип: task\n" +
		"Владелец файлов: internal/domain/types.go\nЗависимости: T3.19\nСсылка: ADR 0032\n" +
		"Агент: agent-1\nВремя: 2026-11-19T14:03:21Z\n" +
		"Цель: цель-intent\nКонтекст: контекст-evidence\nШаги: шаги-patch\n" +
		"Проверка: проверка-verify\nПриёмка: приёмка-risk\n"
	if err := os.WriteFile(filepath.Join(e.tasksDir, "T3.25.md"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}

	want := Handoff{
		Intent:           "цель-intent",
		Evidence:         "контекст-evidence",
		PatchPlan:        "шаги-patch",
		VerificationPlan: "проверка-verify",
		RiskNotes:        "приёмка-risk",
	}
	got, err := e.svc.BuildHandoff("T3.25.md")
	if err != nil || got != want {
		t.Fatalf("BuildHandoff: %+v %v, ждали %+v", got, err, want)
	}

	// Пять ключей контракта и совместимость с domain.Handoff.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Errorf("ключей %d, ждали пять: %s", len(fields), b)
	}
	for _, k := range []string{"intent", "evidence", "patch_plan", "verification_plan", "risk_notes"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("нет ключа %s: %s", k, b)
		}
	}
	var dh domain.Handoff
	if err := json.Unmarshal(b, &dh); err != nil {
		t.Fatalf("несовместимость с domain.Handoff: %v", err)
	}
	if err := dh.Validate(); err != nil {
		t.Errorf("domain.Handoff не прошёл валидацию: %v", err)
	}
	if dh.Intent != want.Intent || dh.RiskNotes != want.RiskNotes {
		t.Errorf("domain.Handoff = %+v, ждали поля из карточки", dh)
	}
}

func TestBuildHandoffRejectsBadInputAndEmptyFields(t *testing.T) {
	e := newEnv(t)
	// Пустое тело карточки: обязательные поля присутствуют, но пусты.
	e.card(t, "T3.26", "in_progress", "task", "ADR 0032", "internal/domain/types.go", "agent-1", "2026-11-19T14:03:21Z")
	path := filepath.Join(e.tasksDir, "T3.26.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	empty := strings.Replace(string(raw), "Проверка: п\n", "Проверка:\n", 1)
	if err := os.WriteFile(path, []byte(empty), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BuildHandoff("T3.26"); taskstore.Code(err) != 1 || !strings.Contains(err.Error(), "verification_plan") {
		t.Errorf("пустое поле передачи дало %v, ждали код 1 с именем поля", err)
	}

	for _, id := range []string{"", "bad", "T9.9"} {
		if _, err := e.svc.BuildHandoff(id); taskstore.Code(err) != 1 {
			t.Errorf("BuildHandoff(%q) дал %v, ждали код 1", id, err)
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

func TestIntersectingFiles(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"разные файлы", []string{"internal/app/a.go"}, []string{"internal/app/b.go"}, false},
		{"один файл", []string{"internal/app/a.go"}, []string{"internal/app/a.go"}, true},
		{"каталог и файл внутри", []string{"internal/app"}, []string{"internal/app/a.go"}, true},
		{"файл внутри каталога", []string{"internal/app/a.go"}, []string{"internal/app"}, true},
		{"разные каталоги", []string{"internal/app"}, []string{"internal/infra"}, false},
		{"пустые назначения", nil, []string{"internal/app/a.go"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb, got := IntersectingFiles(LaunchAssignment{Name: "a", Files: tc.a}, LaunchAssignment{Name: "b", Files: tc.b})
			if got != tc.want {
				t.Fatalf("IntersectingFiles = %v, ждали %v", got, tc.want)
			}
			if got && (fa == "" || fb == "") {
				t.Errorf("пересечение без имён файлов: %q, %q", fa, fb)
			}
		})
	}
}

func TestIntersectingFilesWithGlobs(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		{"glob покрывает файл", []string{"internal/app/taskcli/*.go"}, []string{"internal/app/taskcli/service.go"}, true},
		{"файл и glob в обратном порядке", []string{"internal/app/taskcli/service.go"}, []string{"internal/app/taskcli/*.go"}, true},
		{"два пересекающихся glob", []string{"internal/app/**/*.go"}, []string{"internal/app/taskcli/*.go"}, true},
		{"разные каталоги", []string{"internal/app/taskcli/*.go"}, []string{"internal/infra/mcp/*.go"}, false},
		{"glob не переходит сегмент", []string{"internal/app/*.go"}, []string{"internal/app/sub/service.go"}, false},
		{"разные расширения", []string{"docs/tasks/*.md"}, []string{"docs/tasks/*.go"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb, got := IntersectingFiles(
				LaunchAssignment{Name: "A", Files: tc.a},
				LaunchAssignment{Name: "B", Files: tc.b})
			if got != tc.want {
				t.Fatalf("IntersectingFiles = %v, ждали %v", got, tc.want)
			}
			if got && (fa == "" || fb == "") {
				t.Errorf("пересечение без имён файлов: %q, %q", fa, fb)
			}
		})
	}

	err := CheckLaunch(
		LaunchAssignment{Name: "A", Files: []string{"internal/app/taskcli/*.go"}},
		LaunchAssignment{Name: "B", Files: []string{"internal/app/taskcli/service.go"}},
	)
	if taskstore.Code(err) != 1 || !strings.Contains(err.Error(), "A") || !strings.Contains(err.Error(), "B") {
		t.Fatalf("пересечение glob и файла не остановило запуск: %v", err)
	}
}

func TestCheckLaunchPreStartGate(t *testing.T) {
	disjoint := []LaunchAssignment{
		{Name: "A", Files: []string{"internal/app/a.go"}},
		{Name: "B", Files: []string{"internal/infra/b.go"}},
	}
	if err := CheckLaunch(disjoint...); err != nil {
		t.Fatalf("непересекающиеся назначения дали %v, ждали запуск", err)
	}
	if err := CheckLaunch(disjoint[0]); err != nil {
		t.Fatalf("одно назначение дало %v", err)
	}
	if err := CheckLaunch(); err != nil {
		t.Fatalf("пустой план дал %v", err)
	}

	overlap := []LaunchAssignment{
		{Name: "A", Files: []string{"internal/app/a.go"}},
		{Name: "B", Files: []string{"internal/app"}},
	}
	err := CheckLaunch(overlap...)
	if taskstore.Code(err) != 1 {
		t.Fatalf("пересечение дало %v, ждали код 1", err)
	}
	for _, want := range []string{"A", "B", "internal/app/a.go", "internal/app", "отменён"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}

	// Три назначения: конфликт между непервой и невторой парой тоже блокирует.
	three := []LaunchAssignment{
		{Name: "A", Files: []string{"a.go"}},
		{Name: "B", Files: []string{"b.go"}},
		{Name: "C", Files: []string{"b.go"}},
	}
	if err := CheckLaunch(three...); taskstore.Code(err) != 1 || !strings.Contains(err.Error(), "C") {
		t.Fatalf("конфликт в паре B/C не найден: %v", err)
	}
}

func TestAdmitLaunchRefusesInProgressOverlap(t *testing.T) {
	svc := NewService(Config{})
	quota := PreflightResult{Allowed: true}
	active := LaunchAssignment{Name: "T1", Files: []string{"internal/app/a.go"}, Status: "in_progress"}
	candidate := LaunchAssignment{Name: "T2", Files: []string{"internal/app"}}

	got := svc.AdmitLaunch(quota, 1, 4, active, candidate)
	if got.Allowed {
		t.Fatalf("пересечение с in_progress дало %+v, ждали отказ", got)
	}
	for _, want := range []string{"in_progress", "T1", "T2", "internal/app/a.go"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("в причине отказа нет %q: %s", want, got.Reason)
		}
	}

	// Активная карточка без пересечения не мешает запуску.
	free := LaunchAssignment{Name: "T3", Files: []string{"internal/infra/b.go"}, Status: "in_progress"}
	got = svc.AdmitLaunch(quota, 1, 4, active, free)
	if !got.Allowed || got.Reason != AdmissionAllowed {
		t.Fatalf("непересекающиеся назначения дали %+v, ждали запуск", got)
	}

	// Два уже работающих назначения не являются решением гейта.
	other := LaunchAssignment{Name: "T4", Files: []string{"internal/app"}, Status: "in_progress"}
	got = svc.AdmitLaunch(quota, 2, 4, active, other)
	if !got.Allowed {
		t.Fatalf("две in_progress карточки дали %+v, ждали запуск", got)
	}

	// Пересечение двух кандидатов всё ещё блокируется.
	got = svc.AdmitLaunch(quota, 1, 4,
		LaunchAssignment{Name: "T5", Files: []string{"a.go"}},
		LaunchAssignment{Name: "T6", Files: []string{"a.go"}})
	if got.Allowed || !strings.Contains(got.Reason, "T6") {
		t.Fatalf("пересечение кандидатов дало %+v, ждали отказ", got)
	}
}

func TestAdmitLaunchRefusesQuotaBeforeLaunch(t *testing.T) {
	svc := NewService(Config{})
	conflict := LaunchAssignment{Name: "T1", Files: []string{"a.go"}}
	other := LaunchAssignment{Name: "T2", Files: []string{"a.go"}}

	// Квота проверяется первой: причина квоты важнее конфликта файлов.
	got := svc.AdmitLaunch(PreflightResult{Reason: "quota_exhausted"}, 1, 4, conflict, other)
	if got.Allowed || got.Reason != "quota_exhausted" {
		t.Fatalf("quota_exhausted дал %+v, ждали отказ до запуска", got)
	}
	got = svc.AdmitLaunch(PreflightResult{Reason: "quota_unknown"}, 1, 4)
	if got.Allowed || got.Reason != "quota_unknown" {
		t.Fatalf("quota_unknown дал %+v, ждали отказ", got)
	}
	got = svc.AdmitLaunch(PreflightResult{}, 1, 4, conflict, other)
	if got.Allowed || got.Reason != AdmissionQuotaExhausted {
		t.Fatalf("пустая причина квоты дала %+v, ждали quota_exhausted", got)
	}
}

func TestAdmitLaunchRefusesWhenSlotsFull(t *testing.T) {
	svc := NewService(Config{})
	quota := PreflightResult{Allowed: true}
	for _, tc := range []struct{ used, total int }{
		{4, 4}, {5, 4}, {1, 0}, {1, -1},
	} {
		got := svc.AdmitLaunch(quota, tc.used, tc.total, LaunchAssignment{Name: "T1", Files: []string{"a.go"}})
		if got.Allowed || got.Reason != AdmissionSlotsExhausted {
			t.Errorf("слоты %d/%d дали %+v, ждали %s", tc.used, tc.total, got, AdmissionSlotsExhausted)
		}
	}
	got := svc.AdmitLaunch(quota, 3, 4, LaunchAssignment{Name: "T1", Files: []string{"a.go"}})
	if !got.Allowed || got.Reason != AdmissionAllowed {
		t.Fatalf("свободный слот дал %+v, ждали запуск", got)
	}
}

func TestPlanLaunchReadsCards(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.1", "todo", "task", "ADR 0001", "internal/app/a.go", "нет", "нет")
	e.card(t, "T3.2", "todo", "task", "ADR 0001", "internal/infra/b.go", "нет", "нет")

	if err := e.svc.PlanLaunch("T3.1", "T3.2"); err != nil {
		t.Fatalf("непересекающиеся карточки дали %v, ждали запуск", err)
	}
	if err := e.svc.PlanLaunch("T3.1.md", "T3.2.md"); err != nil {
		t.Fatalf("ID с расширением дали %v", err)
	}

	// Пересечение по каталогу блокирует запуск до снятия конфликта.
	e.card(t, "T3.3", "in_progress", "task", "ADR 0001", "internal/app", "agent-1", "2026-11-19T14:03:21Z")
	err := e.svc.PlanLaunch("T3.1", "T3.3")
	if taskstore.Code(err) != 1 || !strings.Contains(err.Error(), "T3.1") || !strings.Contains(err.Error(), "T3.3") {
		t.Fatalf("пересекающиеся карточки дали %v, ждали код 1 с обоими ID", err)
	}

	// Закрытая карточка не блокирует запуск: её файлы свободны.
	e.card(t, "T3.4", "done", "task", "ADR 0001", "internal/app/a.go", "agent-1", "2026-11-19T14:03:21Z")
	if err := e.svc.PlanLaunch("T3.1", "T3.4"); err != nil {
		t.Fatalf("карточка done должна освобождать файлы: %v", err)
	}
	e.card(t, "T3.5", "dropped", "task", "ADR 0001", "internal/app/a.go", "agent-1", "2026-11-19T14:03:21Z")
	if err := e.svc.PlanLaunch("T3.1", "T3.5"); err != nil {
		t.Fatalf("карточка dropped должна освобождать файлы: %v", err)
	}
}

func TestPlanLaunchBadInput(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.1", "todo", "task", "ADR 0001", "internal/app/a.go", "нет", "нет")
	for _, ids := range [][]string{
		{"bad"},
		{""},
		{"T9.9"},
		{"T3.1", "T3.1"},
		{"T3.1", "T9.9"},
	} {
		if err := e.svc.PlanLaunch(ids...); taskstore.Code(err) != 1 {
			t.Errorf("PlanLaunch(%v) дал %v, ждали код 1", ids, err)
		}
	}
	if err := e.svc.PlanLaunch(); err != nil {
		t.Errorf("PlanLaunch без карточек дал %v", err)
	}
}

func TestPlanLaunchDetectsGlobOverlapBetweenCards(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T7.1", "todo", "task", "ADR 0001", "internal/app/taskcli/service.go", "нет", "нет")
	e.card(t, "T7.2", "todo", "task", "ADR 0001", "internal/app/taskcli/*.go", "нет", "нет")
	e.card(t, "T7.3", "todo", "task", "ADR 0001", "internal/infra/mcp/*.go", "нет", "нет")

	if err := e.svc.PlanLaunch("T7.1", "T7.3"); err != nil {
		t.Fatalf("разные каталоги дали %v, ждали запуск", err)
	}
	err := e.svc.PlanLaunch("T7.1", "T7.2")
	if taskstore.Code(err) != 1 || !strings.Contains(err.Error(), "T7.2") {
		t.Fatalf("пересечение по glob не остановило запуск: %v", err)
	}
}

func TestReserveLeasesWorktreeAndTTL(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := e.svc.ReserveLeases("agent-a",
		[]string{"internal/app/*.go"}, []string{"worktrees/w7-c"}, now); err != nil {
		t.Fatal(err)
	}
	rs, err := e.svc.Reservations()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || !reflect.DeepEqual(rs[0].Globs,
		[]string{"internal/app/*.go", "worktrees/w7-c/**"}) {
		t.Fatalf("аренда ворктри не записана: %+v", rs)
	}

	d, err := e.svc.CheckCommit("agent-b", []string{"worktrees/w7-c/task.go"}, false, now)
	if err != nil || !d.Blocked || !reflect.DeepEqual(d.Owners, []string{"agent-a"}) {
		t.Fatalf("чужая аренда ворктри не заблокировала: %+v err=%v", d, err)
	}

	later := now.Add(LeaseTTL + time.Minute)
	d, err = e.svc.CheckCommit("agent-b", []string{"worktrees/w7-c/task.go"}, false, later)
	if err != nil || d.Blocked {
		t.Fatalf("просроченная аренда заблокировала коммит: %+v err=%v", d, err)
	}
}

// ---- ADR 0037 п2 / T3.48: возврат по событию, не по таймауту опроса ----

func TestAwaitResultReturnsOnResultNotTick(t *testing.T) {
	events := make(chan WaitEvent, 3)
	events <- WaitEvent{Kind: WaitEventTick, WatchID: "watch-1", Revision: 7, Status: "watching", Verdict: "watching"}
	events <- WaitEvent{Kind: WaitEventTick, WatchID: "watch-1", Revision: 8, Status: "watching", Verdict: "watching"}
	events <- WaitEvent{Kind: WaitEventResult, WatchID: "watch-1", Revision: 9, Status: "notifying", Verdict: "ready"}
	close(events)

	out := AwaitResult(events, 5000)
	if out.Cause != WaitEventResult {
		t.Fatalf("причина возврата = %q, ждали result", out.Cause)
	}
	if out.Revision != 9 || out.Verdict != "ready" || out.Status != "notifying" || out.WatchID != "watch-1" {
		t.Fatalf("исход не несёт завершающее событие: %+v", out)
	}
	if out.PollAfterMs != 5000 {
		t.Fatalf("pollAfterMs = %d, ждали 5000", out.PollAfterMs)
	}
}

func TestAwaitResultReturnsAtApprovalBoundary(t *testing.T) {
	events := make(chan WaitEvent, 3)
	events <- WaitEvent{Kind: WaitEventTick, WatchID: "watch-1", Revision: 4, Status: "watching", Verdict: "watching"}
	events <- WaitEvent{Kind: WaitEventWatch, WatchID: "watch-1", Revision: 5, Status: "notifying", Verdict: "watching"}
	events <- WaitEvent{Kind: WaitEventApproval, WatchID: "watch-1", Revision: 6, Status: "needs_input", Verdict: "needs_input"}
	close(events)

	out := AwaitResult(events, 5000)
	if out.Cause != WaitEventApproval {
		t.Fatalf("причина возврата = %q, ждали approval", out.Cause)
	}
	if out.Revision != 6 || out.Status != "needs_input" {
		t.Fatalf("исход не несёт границу одобрения: %+v", out)
	}
}

func TestWaitStateConsumeTickDoesNotEndWait(t *testing.T) {
	var st WaitState
	if out, done := st.Consume(WaitEvent{Kind: WaitEventTick, WatchID: "watch-1", Revision: 7, Status: "watching", Verdict: "watching"}); done {
		t.Fatalf("тик закончил ожидание: %+v", out)
	}
	if out, done := st.Consume(WaitEvent{Kind: WaitEventWatch, WatchID: "watch-1", Revision: 8, Status: "notifying", Verdict: "watching"}); done {
		t.Fatalf("рабочее обновление закончило ожидание: %+v", out)
	}
	if st.Revision != 8 {
		t.Fatalf("наблюдаемая ревизия = %d, ждали 8", st.Revision)
	}

	out, done := st.Consume(WaitEvent{Kind: WaitEventResult, WatchID: "watch-1", Revision: 9, Status: "notifying", Verdict: "ready"})
	if !done || out.Cause != WaitEventResult {
		t.Fatalf("результат не закончил ожидание: done=%v out=%+v", done, out)
	}
	if out.Revision != 9 {
		t.Fatalf("исход несёт ревизию %d, ждали 9", out.Revision)
	}
}

func TestAwaitResultSourceEndedIsNotPollTimeout(t *testing.T) {
	events := make(chan WaitEvent, 1)
	events <- WaitEvent{Kind: WaitEventTick, WatchID: "watch-1", Revision: 7, Status: "watching", Verdict: "watching"}
	close(events)

	out := AwaitResult(events, 5000)
	if out.Cause != WaitEventSourceEnded {
		t.Fatalf("причина = %q, ждали source-ended", out.Cause)
	}
	if out.Cause == WaitEventTick {
		t.Fatal("тик опроса не должен быть причиной возврата")
	}
}

func TestAwaitWithLadderPreservesMarkers(t *testing.T) {
	evidence := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	pos := WaitLadderPosition{
		Outcome:    "transport-failed",
		Attempts:   2,
		EvidenceAt: evidence,
		Stopped:    false,
	}
	before := pos

	events := make(chan WaitEvent, 2)
	events <- WaitEvent{Kind: WaitEventTick, WatchID: "watch-1", Revision: 7, Status: "watching", Verdict: "watching"}
	events <- WaitEvent{Kind: WaitEventApproval, WatchID: "watch-1", Revision: 8, Status: "needs_input", Verdict: "needs_input"}
	close(events)

	out, got := AwaitWithLadder(events, pos, 5000)
	if out.Cause != WaitEventApproval {
		t.Fatalf("причина = %q, ждали approval", out.Cause)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("возврат изменил маркеры лестницы: до %+v, после %+v", before, got)
	}
	if got.Stopped || got.Attempts != 2 || !got.EvidenceAt.Equal(evidence) {
		t.Fatalf("лестница сломана: %+v", got)
	}
	if !LadderRetries(got.Outcome) {
		t.Fatal("transport-failed обязан сохранить повтор по ADR 0017")
	}
}

func TestLadderRetriesBranches(t *testing.T) {
	cases := map[string]bool{
		"posted":           false,
		"transport-failed": true,
		"indeterminate":    false,
	}
	for outcome, want := range cases {
		if got := LadderRetries(outcome); got != want {
			t.Errorf("LadderRetries(%q) = %v, ждали %v", outcome, got, want)
		}
	}
}

func TestWaitOutcomeByteStable(t *testing.T) {
	out := WaitOutcome{
		WatchID: "watch-1", Cause: WaitEventResult, Revision: 9,
		Status: "notifying", Verdict: "ready", PollAfterMs: 5000,
	}
	a, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("байты нестабильны: %s и %s", a, b)
	}
	want := `{"watchID":"watch-1","cause":"result","revision":9,"status":"notifying","verdict":"ready","pollAfterMs":5000}`
	if string(a) != want {
		t.Fatalf("порядок полей изменился: %s", a)
	}
}

// ---- ADR 0038 п3 / T3.55 ----

func TestRankWorkersOrdersByHistoryNotName(t *testing.T) {
	candidates := []WorkerCandidate{
		{Key: "a-first", Metrics: WorkerMetrics{Passes: 5, Failures: 5, DelayMS: 900, Cost: 90, Samples: 1}},
		{Key: "b-second", Metrics: WorkerMetrics{Passes: 5, Failures: 5, DelayMS: 100, Cost: 10, Samples: 1}},
		{Key: "c-third", Metrics: WorkerMetrics{Passes: 9, Failures: 1, DelayMS: 800, Cost: 80, Samples: 1}},
		{Key: "d-fourth", Metrics: WorkerMetrics{Passes: 5, Failures: 5, DelayMS: 100, Cost: 10, Samples: 1}},
	}
	got := RankWorkers(candidates)
	want := []string{"c-third", "b-second", "d-fourth", "a-first"}
	for i, k := range want {
		if got[i].Key != k {
			t.Fatalf("rank[%d]=%q, want %q", i, got[i].Key, k)
		}
	}
}

func TestRankWorkersUsesDelayBeforeCost(t *testing.T) {
	candidates := []WorkerCandidate{
		{Key: "slow-cheap", Metrics: WorkerMetrics{Passes: 2, DelayMS: 500, Cost: 1, Samples: 1}},
		{Key: "fast-costly", Metrics: WorkerMetrics{Passes: 2, DelayMS: 50, Cost: 100, Samples: 1}},
	}
	got := RankWorkers(candidates)
	if got[0].Key != "fast-costly" || got[1].Key != "slow-cheap" {
		t.Fatalf("order=%q,%q; want fast-costly,slow-cheap", got[0].Key, got[1].Key)
	}
}

func TestRankWorkersUsesCostAsLastCriterion(t *testing.T) {
	candidates := []WorkerCandidate{
		{Key: "costly", Metrics: WorkerMetrics{Passes: 3, Failures: 1, DelayMS: 100, Cost: 50, Samples: 1}},
		{Key: "cheap", Metrics: WorkerMetrics{Passes: 3, Failures: 1, DelayMS: 100, Cost: 5, Samples: 1}},
	}
	got := RankWorkers(candidates)
	if got[0].Key != "cheap" || got[1].Key != "costly" {
		t.Fatalf("order=%q,%q; want cheap,costly", got[0].Key, got[1].Key)
	}
}

func TestSelectWorkerMatchesCapabilitiesBeforeRanking(t *testing.T) {
	candidates := []WorkerCandidate{
		{Key: "chat-best-history", Capabilities: []string{"chat"}, Metrics: WorkerMetrics{Passes: 100, Samples: 1}},
		{Key: "review-low", Capabilities: []string{"review"}, Metrics: WorkerMetrics{Passes: 1, Failures: 9, Samples: 10}},
		{Key: "review-high", Capabilities: []string{"review", "build"}, Metrics: WorkerMetrics{Passes: 8, Failures: 2, Samples: 10}},
	}
	got, ok := SelectWorker(candidates, []string{"review"})
	if !ok || got.Key != "review-high" {
		t.Fatalf("select=%q %v; want review-high true", got.Key, ok)
	}
}

func TestSelectWorkerRequiresCapabilitiesAndMatch(t *testing.T) {
	candidates := []WorkerCandidate{
		{Key: "w", Capabilities: []string{"chat"}, Metrics: WorkerMetrics{Passes: 100}},
	}
	if _, ok := SelectWorker(candidates, nil); ok {
		t.Fatal("пустой набор возможностей не должен выбирать воркера")
	}
	if _, ok := SelectWorker(candidates, []string{"review"}); ok {
		t.Fatal("несовпадающие возможности не должны выбирать воркера")
	}
}

func TestSelectWorkerRenamingDoesNotChangeChoice(t *testing.T) {
	candidates := []WorkerCandidate{
		{Key: "alpha", Capabilities: []string{"review"}, Metrics: WorkerMetrics{Passes: 2, Failures: 8, Samples: 10}},
		{Key: "zeta", Capabilities: []string{"review"}, Metrics: WorkerMetrics{Passes: 9, Failures: 1, Samples: 10}},
	}
	first, ok := SelectWorker(candidates, []string{"review"})
	if !ok || first.Key != "zeta" {
		t.Fatalf("first=%q %v; want zeta true", first.Key, ok)
	}
	candidates[1].Key = "renamed-999"
	second, ok := SelectWorker(candidates, []string{"review"})
	if !ok || second.Key != "renamed-999" {
		t.Fatalf("after rename=%q %v; want renamed-999 true", second.Key, ok)
	}
}

func TestWorkerHistoryRecordLoadAttachAndSelect(t *testing.T) {
	e := newEnv(t)
	h, err := e.svc.LoadWorkerHistory()
	if err != nil || len(h) != 0 {
		t.Fatalf("initial history=%v err=%v; want empty", h, err)
	}
	for _, o := range []WorkerOutcome{
		{Worker: "w1", Passed: true, DelayMS: 200, Cost: 10},
		{Worker: "w1", Passed: false, DelayMS: 100, Cost: 5},
		{Worker: "w2", Passed: true, DelayMS: 20, Cost: 2},
	} {
		if err := e.svc.RecordWorkerOutcome(o); err != nil {
			t.Fatalf("RecordWorkerOutcome(%+v) error = %v", o, err)
		}
	}
	h, err = e.svc.LoadWorkerHistory()
	if err != nil {
		t.Fatal(err)
	}
	w1 := h["w1"]
	if w1.Passes != 1 || w1.Failures != 1 || w1.DelayMS != 300 || w1.Cost != 15 || w1.Samples != 2 {
		t.Fatalf("w1=%+v; want 1/1/300/15/2", w1)
	}
	candidates := []WorkerCandidate{
		{Key: "w1", Capabilities: []string{"review"}},
		{Key: "w2", Capabilities: []string{"review"}},
		{Key: "new", Capabilities: []string{"review"}},
	}
	withHistory, err := e.svc.AttachHistory(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if withHistory[0].Metrics.Samples != 2 || withHistory[2].Metrics.Samples != 0 {
		t.Fatalf("attach=%+v; w1 should have history, new should not", withHistory)
	}
	sel, ok, err := e.svc.SelectWorkerByHistory(candidates, []string{"review"})
	if err != nil || !ok {
		t.Fatalf("SelectWorkerByHistory err=%v ok=%v", err, ok)
	}
	if sel.Key != "w2" {
		t.Fatalf("selected=%q, want w2 (best pass history)", sel.Key)
	}
}

func TestRecordWorkerOutcomeRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	if got := taskstore.Code(e.svc.RecordWorkerOutcome(WorkerOutcome{Worker: "  ", Passed: true})); got != 1 {
		t.Fatalf("пустой воркер: код %d, want 1", got)
	}
	if got := taskstore.Code(e.svc.RecordWorkerOutcome(WorkerOutcome{Worker: "w", DelayMS: -1})); got != 1 {
		t.Fatalf("отрицательная задержка: код %d, want 1", got)
	}
	if got := taskstore.Code(e.svc.RecordWorkerOutcome(WorkerOutcome{Worker: "w", Cost: -1})); got != 1 {
		t.Fatalf("отрицательная стоимость: код %d, want 1", got)
	}
}

// ---- ADR 0039 п2 / T3.61: классификация, bounded retry и общий потолок ----

func TestClassifyCheck(t *testing.T) {
	cases := []struct {
		name string
		f    CheckFailure
		want CheckClass
		heal bool
	}{
		{"timeout", CheckFailure{Name: "unit", Output: "HTTP call timed out after 30s"}, CheckClassTransient, true},
		{"429", CheckFailure{Name: "deploy", Output: "error 429 Too Many Requests"}, CheckClassTransient, true},
		{"reset", CheckFailure{Name: "e2e", Output: "ECONNRESET socket hang up"}, CheckClassTransient, true},
		{"assertion", CheckFailure{Name: "unit", Output: "--- FAIL: TestX assertion failed"}, CheckClassDeterministic, false},
		{"compile", CheckFailure{Name: "build", Output: "compilation error: undefined symbol"}, CheckClassDeterministic, false},
		{"unknown", CheckFailure{Name: "custom", Output: "непонятный вывод проверки"}, CheckClassUnknown, false},
	}
	for _, tc := range cases {
		got := ClassifyCheck(tc.f)
		if got != tc.want {
			t.Errorf("%s: class=%q, want %q", tc.name, got, tc.want)
		}
		if got.Healable() != tc.heal {
			t.Errorf("%s: healable=%v, want %v", tc.name, got.Healable(), tc.heal)
		}
	}
}

func TestDecideHealingRetriesThenParksOnAttemptLimit(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := CheckFailure{Name: "unit", Output: "connection reset by peer"}
	for i := 1; i <= MaxHealingAttempts; i++ {
		d, err := e.svc.DecideHealing("T3.61", f, now)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != HealingRetry || d.Attempt != i || d.Class != CheckClassTransient {
			t.Fatalf("попытка %d: решение %+v", i, d)
		}
		if d.CapSpent != i || d.CapLimit != HourlyLaunchCap {
			t.Fatalf("попытка %d: расход %+v", i, d)
		}
	}
	d, err := e.svc.DecideHealing("T3.61", f, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != HealingPark || !strings.Contains(d.Reason, "попытки") {
		t.Fatalf("исчерпание попыток: решение %+v", d)
	}
	if d.CapSpent != MaxHealingAttempts {
		t.Fatalf("парковка увеличила расход: %+v", d)
	}
	att, err := e.svc.HealingAttempts("T3.61", "unit")
	if err != nil || att != MaxHealingAttempts {
		t.Fatalf("attempts=%d err=%v", att, err)
	}
	log, err := e.svc.HealingLog()
	if err != nil || len(log) != MaxHealingAttempts+1 {
		t.Fatalf("log len=%d err=%v", len(log), err)
	}
	last := log[len(log)-1]
	if last.Class != CheckClassTransient || last.Action != HealingPark || last.Output != f.Output ||
		last.Attempt != MaxHealingAttempts || last.CapSpent != MaxHealingAttempts {
		t.Fatalf("последняя запись журнала: %+v", last)
	}
	spent, err := e.svc.CapSpent(now)
	if err != nil || spent != MaxHealingAttempts {
		t.Fatalf("cap spent=%d err=%v", spent, err)
	}
}

func TestDecideHealingHourlyCapIsServiceWide(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := CheckFailure{Name: "lint", Output: "service temporarily unavailable"}
	for i := 0; i < HourlyLaunchCap; i++ {
		d, err := e.svc.DecideHealing(fmt.Sprintf("T9.%d", i+1), f, now)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != HealingRetry || d.CapSpent != i+1 {
			t.Fatalf("запуск %d: решение %+v", i+1, d)
		}
	}
	spent, err := e.svc.CapSpent(now)
	if err != nil || spent != HourlyLaunchCap {
		t.Fatalf("cap spent=%d err=%v", spent, err)
	}
	d, err := e.svc.DecideHealing("T9.99", f, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != HealingPark || !strings.Contains(d.Reason, "потолок") {
		t.Fatalf("исчерпание потолка: решение %+v", d)
	}
	if d.CapSpent != HourlyLaunchCap || d.CapLimit != HourlyLaunchCap {
		t.Fatalf("расход потолка: решение %+v", d)
	}
	spent, err = e.svc.CapSpent(now)
	if err != nil || spent != HourlyLaunchCap {
		t.Fatalf("парковка увеличила расход: spent=%d err=%v", spent, err)
	}
}

func TestDecideHealingHourlyWindowPrunesOldLaunches(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	f := CheckFailure{Name: "e2e", Output: "429 rate limit"}
	for i := 0; i < HourlyLaunchCap; i++ {
		if _, err := e.svc.DecideHealing(fmt.Sprintf("T8.%d", i+1), f, old); err != nil {
			t.Fatal(err)
		}
	}
	d, err := e.svc.DecideHealing("T8.99", f, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != HealingRetry || d.CapSpent != 1 {
		t.Fatalf("окно часа: решение %+v", d)
	}
}

func TestDecideHealingNonHealableParksWithoutSpendingCap(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, output string
		class        CheckClass
	}{
		{"unit", "--- FAIL: TestX assertion failed", CheckClassDeterministic},
		{"custom", "непонятный вывод проверки", CheckClassUnknown},
	}
	for _, tc := range cases {
		d, err := e.svc.DecideHealing("T7.1", CheckFailure{Name: tc.name, Output: tc.output}, now)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != HealingPark || d.Class != tc.class || d.CapSpent != 0 || d.Attempt != 0 {
			t.Fatalf("%s: решение %+v", tc.name, d)
		}
	}
	spent, err := e.svc.CapSpent(now)
	if err != nil || spent != 0 {
		t.Fatalf("нелечимые проверки потратили потолок: spent=%d err=%v", spent, err)
	}
	log, err := e.svc.HealingLog()
	if err != nil || len(log) != 2 || log[0].Output != cases[0].output || log[1].Output != cases[1].output {
		t.Fatalf("журнал не сохранил полный текст: log=%+v err=%v", log, err)
	}
}

func TestDecideHealingPersistsAcrossServicesAndValidates(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	_, err := e.svc.DecideHealing("  ", CheckFailure{Name: "unit", Output: "timeout"}, now)
	if taskstore.Code(err) != 1 {
		t.Fatalf("пустая задача: код %d, err=%v", taskstore.Code(err), err)
	}
	f := CheckFailure{Name: "unit", Output: "timeout"}
	if _, err := e.svc.DecideHealing("T6.1", f, now); err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(e.cfg)
	spent, err := svc2.CapSpent(now)
	if err != nil || spent != 1 {
		t.Fatalf("новый сервис не видит расход: spent=%d err=%v", spent, err)
	}
	d, err := svc2.DecideHealing("T6.2", f, now)
	if err != nil || d.Action != HealingRetry || d.CapSpent != 2 {
		t.Fatalf("новый сервис: решение %+v err=%v", d, err)
	}
}

func TestDecideHealingConcurrentCapNeverExceeded(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := CheckFailure{Name: "e2e", Output: "429 rate limit"}
	const n = HourlyLaunchCap + 4
	results := make(chan HealingDecision, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			d, err := e.svc.DecideHealing(fmt.Sprintf("T5.%d", i+1), f, now)
			if err != nil {
				errs <- err
				return
			}
			results <- d
		}(i)
	}
	retries := 0
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			t.Fatal(err)
		case d := <-results:
			if d.CapSpent > HourlyLaunchCap {
				t.Fatalf("потолок превышен: %+v", d)
			}
			switch d.Action {
			case HealingRetry:
				retries++
			case HealingPark:
				if !strings.Contains(d.Reason, "потолок") {
					t.Fatalf("парковка не по потолку: %+v", d)
				}
			}
		}
	}
	if retries != HourlyLaunchCap {
		t.Fatalf("ретраев %d, want %d", retries, HourlyLaunchCap)
	}
	spent, err := e.svc.CapSpent(now)
	if err != nil || spent != HourlyLaunchCap {
		t.Fatalf("итоговый расход=%d err=%v", spent, err)
	}
}

func TestGlobMatchesReservationBoundaries(t *testing.T) {
	cases := []struct {
		glob, file string
		want       bool
	}{
		{"internal/app/taskcli/service.go", "internal/app/taskcli/service.go", true},
		{"internal/app/taskcli", "internal/app/taskcli/service.go", true},
		{"internal/app/taskcli/", "internal/app/taskcli/service.go", true},
		{"internal/app/taskcli/*.go", "internal/app/taskcli/service.go", true},
		{"internal/app/taskcli/*.go", "internal/app/taskcli/sub/service.go", false},
		{"internal/app/**/*.go", "internal/app/taskcli/service.go", true},
		{"internal/app/**/*.go", "internal/app/service.go", true},
		{"internal/app/**", "internal/app/taskcli/service.go", true},
		{"*.md", "docs/tasks/T3.71.md", false},
		{"docs/tasks/*.md", "docs/tasks/T3.71.md", true},
		{"internal\\app\\taskcli\\*.go", "internal/app/taskcli/service.go", true},
		{"internal/app/a.go", "internal/app/ab.go", false},
	}
	for _, tc := range cases {
		if got := globMatches(tc.glob, tc.file); got != tc.want {
			t.Errorf("globMatches(%q, %q)=%v, want %v", tc.glob, tc.file, got, tc.want)
		}
	}
}

func TestReserveFilesAndGuardBlocksOtherAgent(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := e.svc.ReserveFiles("agent-a", []string{"internal/app/taskcli/*.go"}, now); err != nil {
		t.Fatal(err)
	}

	d, err := e.svc.CheckCommit("agent-b", []string{"internal/app/taskcli/service.go"}, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Blocked || d.Allowed || d.Bypassed {
		t.Fatalf("чужая аренда не заблокировала коммит: %+v", d)
	}
	if !reflect.DeepEqual(d.Files, []string{"internal/app/taskcli/service.go"}) || !reflect.DeepEqual(d.Owners, []string{"agent-a"}) {
		t.Fatalf("неверные файлы или владельцы: %+v", d)
	}

	// Своя аренда коммит не блокирует.
	d, err = e.svc.CheckCommit("agent-a", []string{"internal/app/taskcli/service.go"}, false, now)
	if err != nil || d.Blocked || !d.Allowed {
		t.Fatalf("своя аренда заблокировала коммит: %+v err=%v", d, err)
	}

	// Файл вне аренды не блокируется.
	d, err = e.svc.CheckCommit("agent-b", []string{"internal/app/other/one.go"}, false, now)
	if err != nil || d.Blocked || !d.Allowed {
		t.Fatalf("посторонний файл заблокирован: %+v err=%v", d, err)
	}

	log, err := e.svc.ReservationLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Kind != ReservationAcquire || log[1].Kind != ReservationBlocked {
		t.Fatalf("журнал аренды: %+v", log)
	}
	if log[1].Agent != "agent-b" || !reflect.DeepEqual(log[1].Owners, []string{"agent-a"}) {
		t.Fatalf("запись блокировки: %+v", log[1])
	}
}

func TestCheckCommitExplicitBypassIsLogged(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := e.svc.ReserveFiles("agent-a", []string{"docs/tasks/*.md"}, now); err != nil {
		t.Fatal(err)
	}

	d, err := e.svc.CheckCommit("agent-b", []string{"docs/tasks/T3.71.md"}, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Blocked || !d.Bypassed || !d.Allowed {
		t.Fatalf("явный обход не пропустил коммит: %+v", d)
	}
	log, err := e.svc.ReservationLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[1].Kind != ReservationBypass || log[1].Agent != "agent-b" {
		t.Fatalf("обход не записан в журнал: %+v", log)
	}
	if !reflect.DeepEqual(log[1].Files, []string{"docs/tasks/T3.71.md"}) || !reflect.DeepEqual(log[1].Owners, []string{"agent-a"}) {
		t.Fatalf("запись обхода: %+v", log[1])
	}
}

func TestReleaseFilesRemovesReservationAndLogsIt(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	globs := []string{"internal/app/taskcli/*.go", "docs/tasks/*.md"}
	if err := e.svc.ReserveFiles("agent-a", globs, now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ReleaseFiles("agent-a", now); err != nil {
		t.Fatal(err)
	}
	rs, err := e.svc.Reservations()
	if err != nil || len(rs) != 0 {
		t.Fatalf("аренда не снята: %+v err=%v", rs, err)
	}
	d, err := e.svc.CheckCommit("agent-b", []string{"internal/app/taskcli/service.go"}, false, now)
	if err != nil || d.Blocked || !d.Allowed {
		t.Fatalf("после снятия коммит заблокирован: %+v err=%v", d, err)
	}
	log, err := e.svc.ReservationLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[1].Kind != ReservationRelease || log[1].Agent != "agent-a" {
		t.Fatalf("снятие не записано: %+v", log)
	}
	if !reflect.DeepEqual(log[1].Globs, globs) {
		t.Fatalf("в журнале сняты не те glob-ы: %+v", log[1])
	}
}

func TestReservationsAreAdvisoryAndSharedAcrossServices(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := e.svc.ReserveFiles("agent-a", []string{"internal/app/one/*.go"}, now); err != nil {
		t.Fatal(err)
	}
	// Вторая аренда того же агента заменяет первую, чужая остаётся.
	if err := e.svc.ReserveFiles("agent-a", []string{"internal/app/two/*.go"}, now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ReserveFiles("agent-c", []string{"docs/adrs/*.md"}, now); err != nil {
		t.Fatal(err)
	}
	other := NewService(e.cfg)
	d, err := other.CheckCommit("agent-b", []string{"internal/app/one/service.go"}, false, now)
	if err != nil || d.Blocked {
		t.Fatalf("заменённая аренда всё ещё блокирует: %+v err=%v", d, err)
	}
	d, err = other.CheckCommit("agent-b", []string{"internal/app/two/service.go"}, false, now)
	if err != nil || !d.Blocked || !reflect.DeepEqual(d.Owners, []string{"agent-a"}) {
		t.Fatalf("общая аренда не видна второму сервису: %+v err=%v", d, err)
	}
	rs, err := other.Reservations()
	if err != nil || len(rs) != 2 {
		t.Fatalf("аренды: %+v err=%v", rs, err)
	}
}

func TestReservationValidation(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	badReserve := []struct {
		agent string
		globs []string
	}{
		{"", []string{"internal/app/a.go"}},
		{"agent-a", nil},
		{"agent-a", []string{"  "}},
		{"agent-a", []string{"internal/app/a.go", ""}},
	}
	for _, tc := range badReserve {
		if err := e.svc.ReserveFiles(tc.agent, tc.globs, now); taskstore.Code(err) != 1 {
			t.Errorf("ReserveFiles(%q, %v)=%v, ждали код 1", tc.agent, tc.globs, err)
		}
	}
	if err := e.svc.ReserveFiles("agent-a", []string{"internal/app/a.go"}, now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ReleaseFiles("", now); taskstore.Code(err) != 1 {
		t.Errorf("ReleaseFiles с пустым агентом=%v, ждали код 1", err)
	}
	if _, err := e.svc.CheckCommit("", []string{"internal/app/a.go"}, false, now); taskstore.Code(err) != 1 {
		t.Errorf("CheckCommit с пустым агентом=%v, ждали код 1", err)
	}
	if _, err := e.svc.CheckCommit("agent-b", nil, false, now); taskstore.Code(err) != 1 {
		t.Errorf("CheckCommit без файлов=%v, ждали код 1", err)
	}
	rs, err := e.svc.Reservations()
	if err != nil || len(rs) != 1 {
		t.Fatalf("неверные проверки изменили аренды: %+v err=%v", rs, err)
	}
}

func TestConcurrentBypassesAreAllLogged(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := e.svc.ReserveFiles("agent-a", []string{"internal/app/one/*.go"}, now); err != nil {
		t.Fatal(err)
	}
	const n = 12
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := e.svc.CheckCommit(fmt.Sprintf("agent-%d", i+2),
				[]string{"internal/app/one/service.go"}, true, now)
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	log, err := e.svc.ReservationLog()
	if err != nil {
		t.Fatal(err)
	}
	bypasses := 0
	for _, entry := range log {
		if entry.Kind == ReservationBypass {
			bypasses++
		}
	}
	if bypasses != n {
		t.Fatalf("в журнале %d обходов, ждали %d", bypasses, n)
	}
}

// ---- ADR 0044 п4 / T3.99: bounded handoff journal с recovery на старте ----

func TestAppendHandoffStepAndRecoverOnStartup(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	steps := []struct{ step, note string }{
		{"read-card", "прочитал карточку"},
		{"plan-patch", "наметил правку"},
		{"run-tests", "прогнал тест в CI"},
	}
	want := make([]HandoffJournalEntry, 0, len(steps))
	for i, s := range steps {
		entry, err := e.svc.AppendHandoffStep("T3.99", s.step, s.note, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if entry.Seq != uint64(i+1) {
			t.Fatalf("шаг %d: seq %d, ждали %d", i, entry.Seq, i+1)
		}
		want = append(want, entry)
	}

	journal, err := e.svc.HandoffJournal()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("журнал: %+v, ждали %+v", journal, want)
	}

	// Свежий сервис с тем же каталогом состояния это старт процесса: он читает
	// журнал и подставляет блок восстановления.
	startup := NewService(e.cfg)
	rec, err := startup.RecoverHandoff()
	if err != nil {
		t.Fatal(err)
	}
	if !rec.HasJournal || rec.Count != len(want) || rec.LastSeq != uint64(len(want)) {
		t.Fatalf("блок восстановления: %+v", rec)
	}
	if !reflect.DeepEqual(rec.Entries, want) {
		t.Fatalf("entries восстановления: %+v, ждали %+v", rec.Entries, want)
	}
	wantSteps := []string{"read-card", "plan-patch", "run-tests"}
	if !reflect.DeepEqual(rec.Steps, wantSteps) {
		t.Fatalf("steps восстановления: %v, ждали %v", rec.Steps, wantSteps)
	}
}

func TestHandoffRecoveryEmptyWithoutJournal(t *testing.T) {
	e := newEnv(t)
	rec, err := e.svc.RecoverHandoff()
	if err != nil {
		t.Fatal(err)
	}
	if rec.HasJournal || rec.Count != 0 || rec.LastSeq != 0 || len(rec.Entries) != 0 || len(rec.Steps) != 0 {
		t.Fatalf("пустой журнал дал блок: %+v", rec)
	}
	journal, err := e.svc.HandoffJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 0 {
		t.Fatalf("пустой журнал вернул записи: %+v", journal)
	}
}

func TestHandoffJournalDropsOldestOnCountOverflow(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	total := MaxHandoffJournalEntries + 5
	for i := 0; i < total; i++ {
		if _, err := e.svc.AppendHandoffStep("T3.99", fmt.Sprintf("step-%d", i+1), "", now); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := e.svc.HandoffJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != MaxHandoffJournalEntries {
		t.Fatalf("в журнале %d записей, лимит %d", len(journal), MaxHandoffJournalEntries)
	}
	// Seq не переиспользуется: отброшены шаги 1..5, остались 6..total.
	if journal[0].Seq != 6 || journal[len(journal)-1].Seq != uint64(total) {
		t.Fatalf("после отбрасывания seq от %d до %d, ждали %d..%d",
			journal[0].Seq, journal[len(journal)-1].Seq, 6, total)
	}
	if journal[0].Step != "step-6" || journal[len(journal)-1].Step != fmt.Sprintf("step-%d", total) {
		t.Fatalf("не те шаги остались: первый %s, последний %s",
			journal[0].Step, journal[len(journal)-1].Step)
	}
}

func TestHandoffJournalDropsOldestOnByteOverflow(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	big := strings.Repeat("x", 40*1024)
	first, err := e.svc.AppendHandoffStep("T3.99", "first", big, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.AppendHandoffStep("T3.99", "second", big, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seq: %d и %d, ждали 1 и 2", first.Seq, second.Seq)
	}
	journal, err := e.svc.HandoffJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 1 {
		t.Fatalf("после переполнения размером в журнале %d записей, ждали 1", len(journal))
	}
	if journal[0].Seq != 2 || journal[0].Step != "second" {
		t.Fatalf("осталась не самая свежая запись: %+v", journal[0])
	}
}

func TestAppendHandoffStepValidatesInput(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.AppendHandoffStep("  ", "step", "note", time.Time{}); taskstore.Code(err) != 1 {
		t.Fatalf("пустая задача: код %d, ждали 1", taskstore.Code(err))
	}
	if _, err := e.svc.AppendHandoffStep("T3.99", "  ", "note", time.Time{}); taskstore.Code(err) != 1 {
		t.Fatalf("пустой шаг: код %d, ждали 1", taskstore.Code(err))
	}
	journal, err := e.svc.HandoffJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 0 {
		t.Fatalf("неверные шаги попали в журнал: %+v", journal)
	}
}

func TestConcurrentHandoffAppendsKeepUniqueSeq(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	const n = 12
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := e.svc.AppendHandoffStep("T3.99", fmt.Sprintf("step-%d", i), "", now)
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	journal, err := e.svc.HandoffJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != n {
		t.Fatalf("в журнале %d записей, ждали %d", len(journal), n)
	}
	seen := make(map[uint64]bool, n)
	for _, entry := range journal {
		if seen[entry.Seq] {
			t.Fatalf("повторный seq %d", entry.Seq)
		}
		seen[entry.Seq] = true
	}
}

func TestHandoffJournalDoesNotReplaceStructuredHandoff(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.99", "todo", "task", "ADR 0001", "internal/app/w211/x.go", "нет", "нет")
	// T3.25 пятиполевая передача читается из карточки и не зависит от журнала.
	h, err := e.svc.BuildHandoff("T3.99")
	if err != nil {
		t.Fatal(err)
	}
	if h.Intent != "ц" || h.Evidence != "к" || h.PatchPlan != "ш" ||
		h.VerificationPlan != "п" || h.RiskNotes != "пр" {
		t.Fatalf("structured handoff: %+v", h)
	}
	if _, err := e.svc.AppendHandoffStep("T3.99", "step", "note", time.Time{}); err != nil {
		t.Fatal(err)
	}
	after, err := e.svc.BuildHandoff("T3.99")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, h) {
		t.Fatalf("журнал изменил структурированную передачу: %+v против %+v", after, h)
	}
}

func (e *env) worktree(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(e.cfg.Root, "worktrees", name)
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRequestFixChecksReusesExistingWorktreeAndAgent(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.50", "in_progress", "task", "ADR 0001", "internal/app/w/x.go", "agent-1", "2026-11-20T10:00:00Z")
	wt := e.worktree(t, "w237")
	before, err := os.ReadDir(e.tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	res, err := e.svc.RequestFixChecks(FixChecksRequest{
		SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1",
		Checks: []string{"go test ./internal/app/taskcli", "gofmt -l ."}, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created {
		t.Fatalf("Created=false, ждалиtrue: %+v", res)
	}
	if res.Task.ID == "" || res.Task.Key == "" {
		t.Fatalf("пустые ID или ключ: %+v", res.Task)
	}
	if res.Task.SourceTaskID != "T3.50" || res.Task.Worktree != wt || res.Task.Agent != "agent-1" {
		t.Fatalf("задача не привязана к источнику, worktree и агенту: %+v", res.Task)
	}
	if res.Task.Status != FixChecksPending || !res.Task.CreatedAt.Equal(now) {
		t.Fatalf("статус или время задачи: %+v", res.Task)
	}
	wantChecks := []string{"go test ./internal/app/taskcli", "gofmt -l ."}
	if !reflect.DeepEqual(res.Task.Checks, wantChecks) {
		t.Fatalf("проверки = %v, ждали %v", res.Task.Checks, wantChecks)
	}
	after, err := os.ReadDir(e.tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("появилась новая карточка: было %d, стало %d", len(before), len(after))
	}
	raw, err := os.ReadFile(filepath.Join(e.tasksDir, "T3.50.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Агент: agent-1") {
		t.Fatalf("агент исходной карточки изменился: %s", raw)
	}
	if info, err := os.Stat(wt); err != nil || !info.IsDir() {
		t.Fatalf("worktree не остался на месте: %v", err)
	}
	tasks, err := e.svc.FixChecksForWorktree(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != res.Task.ID {
		t.Fatalf("задачи worktree: %+v", tasks)
	}
	byID, err := e.svc.FixChecksTask(res.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.ID != res.Task.ID || byID.Key != res.Task.Key || !byID.CreatedAt.Equal(now) {
		t.Fatalf("чтение по ID: %+v", byID)
	}
}

func TestRequestFixChecksIsIdempotentAndNormalizesChecks(t *testing.T) {
	e := newEnv(t)
	wt := e.worktree(t, "w237")
	req := FixChecksRequest{
		SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1",
		Checks: []string{"b", "a", "a"}, Now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
	}
	first, err := e.svc.RequestFixChecks(req)
	if err != nil {
		t.Fatal(err)
	}
	again := req
	again.Checks = []string{"a", "b"}
	second, err := e.svc.RequestFixChecks(again)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created {
		t.Fatalf("Created: первый %v, второй %v", first.Created, second.Created)
	}
	if first.Task.ID != second.Task.ID || first.Task.Key != second.Task.Key {
		t.Fatalf("дубликат задачи: %+v и %+v", first.Task, second.Task)
	}
	if !reflect.DeepEqual(first.Task.Checks, []string{"a", "b"}) {
		t.Fatalf("проверки не нормализованы: %v", first.Task.Checks)
	}
	tasks, err := e.svc.FixChecksTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("задач %d, ждали 1", len(tasks))
	}
}

func TestRequestFixChecksSeparatesDifferentChecks(t *testing.T) {
	e := newEnv(t)
	wt := e.worktree(t, "w237")
	base := FixChecksRequest{SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1"}
	a := base
	a.Checks = []string{"go test ./..."}
	b := base
	b.Checks = []string{"go vet ./..."}
	ra, err := e.svc.RequestFixChecks(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := e.svc.RequestFixChecks(b)
	if err != nil {
		t.Fatal(err)
	}
	if !ra.Created || !rb.Created || ra.Task.ID == rb.Task.ID {
		t.Fatalf("разные проверки дали одну задачу: %+v и %+v", ra.Task, rb.Task)
	}
	tasks, err := e.svc.FixChecksForWorktree(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("задач в worktree %d, ждали 2", len(tasks))
	}
}

func TestRequestFixChecksRejectsMissingWorktree(t *testing.T) {
	e := newEnv(t)
	missing := filepath.Join(e.cfg.Root, "worktrees", "w999")
	req := FixChecksRequest{SourceTaskID: "T3.50", Worktree: missing, Agent: "agent-1", Checks: []string{"go test"}}
	if _, err := e.svc.RequestFixChecks(req); taskstore.Code(err) != 1 {
		t.Fatalf("несуществующий worktree: код %d, ждали 1", taskstore.Code(err))
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("сервис создал worktree: %v", err)
	}
	file := filepath.Join(e.cfg.Root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Worktree = file
	if _, err := e.svc.RequestFixChecks(req); taskstore.Code(err) != 1 {
		t.Fatalf("файл вместо worktree: код %d, ждали 1", taskstore.Code(err))
	}
	tasks, err := e.svc.FixChecksTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("задачи записаны при плохом worktree: %+v", tasks)
	}
}

func TestRequestFixChecksRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	wt := e.worktree(t, "w237")
	base := FixChecksRequest{SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1", Checks: []string{"go test"}}
	cases := []FixChecksRequest{
		func() FixChecksRequest { r := base; r.SourceTaskID = "  "; return r }(),
		func() FixChecksRequest { r := base; r.Agent = "  "; return r }(),
		func() FixChecksRequest { r := base; r.Worktree = "  "; return r }(),
		func() FixChecksRequest { r := base; r.Checks = nil; return r }(),
		func() FixChecksRequest { r := base; r.Checks = []string{"go test", " "}; return r }(),
	}
	for i, r := range cases {
		if _, err := e.svc.RequestFixChecks(r); taskstore.Code(err) != 1 {
			t.Fatalf("кейс %d: код %d, ждали 1", i, taskstore.Code(err))
		}
	}
	tasks, err := e.svc.FixChecksTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("плохие запросы записаны: %+v", tasks)
	}
}

func TestRequestFixChecksRejectsAgentRestartInSameWorktree(t *testing.T) {
	e := newEnv(t)
	wt := e.worktree(t, "w237")
	req := FixChecksRequest{SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1", Checks: []string{"go test"}}
	first, err := e.svc.RequestFixChecks(req)
	if err != nil {
		t.Fatal(err)
	}
	restart := req
	restart.Agent = "agent-2"
	if _, err := e.svc.RequestFixChecks(restart); taskstore.Code(err) != 1 {
		t.Fatalf("перезапуск агента: код %d, ждали 1", taskstore.Code(err))
	}
	tasks, err := e.svc.FixChecksTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Agent != "agent-1" {
		t.Fatalf("агент заменён: %+v", tasks)
	}
	if _, err := e.svc.CompleteFixChecks(first.Task.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	after, err := e.svc.RequestFixChecks(restart)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Created || after.Task.ID == first.Task.ID || after.Task.Agent != "agent-2" {
		t.Fatalf("задача после завершения: %+v", after)
	}
}

func TestCompleteFixChecksAndPersistence(t *testing.T) {
	e := newEnv(t)
	wt := e.worktree(t, "w237")
	res, err := e.svc.RequestFixChecks(FixChecksRequest{
		SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1", Checks: []string{"go test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	done, err := e.svc.CompleteFixChecks(res.Task.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != FixChecksDone {
		t.Fatalf("статус после Complete: %s", done.Status)
	}
	again, err := e.svc.CompleteFixChecks(res.Task.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != FixChecksDone {
		t.Fatalf("повторный Complete изменил статус: %s", again.Status)
	}
	if _, err := e.svc.CompleteFixChecks("  ", time.Time{}); taskstore.Code(err) != 1 {
		t.Fatalf("пустой ID: код %d, ждали 1", taskstore.Code(err))
	}
	if _, err := e.svc.CompleteFixChecks("fix-99", time.Time{}); taskstore.Code(err) != 1 {
		t.Fatalf("неизвестный ID: код %d, ждали 1", taskstore.Code(err))
	}
	other := NewService(e.cfg)
	tasks, err := other.FixChecksTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != res.Task.ID || tasks[0].Status != FixChecksDone {
		t.Fatalf("состояние не пережило новый сервис: %+v", tasks)
	}
}

func TestConcurrentFixChecksRequestsKeepOneTask(t *testing.T) {
	e := newEnv(t)
	wt := e.worktree(t, "w237")
	req := FixChecksRequest{SourceTaskID: "T3.50", Worktree: wt, Agent: "agent-1", Checks: []string{"go test"}}
	const n = 12
	type outcome struct {
		res FixChecksResult
		err error
	}
	outcomes := make(chan outcome, n)
	for i := 0; i < n; i++ {
		go func() {
			res, err := e.svc.RequestFixChecks(req)
			outcomes <- outcome{res: res, err: err}
		}()
	}
	created, ids := 0, make(map[string]bool, n)
	for i := 0; i < n; i++ {
		o := <-outcomes
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.res.Created {
			created++
		}
		ids[o.res.Task.ID] = true
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("создано %d, уникальных ID %d", created, len(ids))
	}
	tasks, err := e.svc.FixChecksTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("задач %d, ждали 1", len(tasks))
	}
}

// ---- ADR 0063 фича 5 / T3.157: подтверждение continued после handoff ----

func TestHandoffCompletedOnlyAfterContinued(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T3.157", "in_progress", "task", "ADR 0063", "internal/app/taskcli/service.go", "agent-1", "2026-11-20T10:00:00Z")

	// Отправка заметки сама по себе handoff не завершает.
	if _, err := e.svc.BuildHandoff("T3.157"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AppendHandoffStep("T3.157", "send-note", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.HandoffStatus("T3.157")
	if err != nil {
		t.Fatal(err)
	}
	if st.Completed || st.Answer != "" || len(st.Answers) != 0 {
		t.Fatalf("отправка заметки завершила handoff: %+v", st)
	}

	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if _, err := e.svc.RecordHandoffAnswer("T3.157", HandoffInconclusive, at); err != nil {
		t.Fatal(err)
	}
	st, err = e.svc.HandoffStatus("T3.157")
	if err != nil {
		t.Fatal(err)
	}
	if st.Completed || st.Answer != HandoffInconclusive || len(st.Answers) != 1 {
		t.Fatalf("inconclusive завершил handoff: %+v", st)
	}
	if _, err := e.svc.RecordHandoffAnswer("T3.157", HandoffNotContinued, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st, err = e.svc.HandoffStatus("T3.157")
	if err != nil {
		t.Fatal(err)
	}
	if st.Completed || st.Answer != HandoffNotContinued || len(st.Answers) != 2 {
		t.Fatalf("not-continued завершил handoff: %+v", st)
	}

	if _, err := e.svc.RecordHandoffAnswer("T3.157", HandoffContinued, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	st, err = e.svc.HandoffStatus("T3.157")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Completed || st.Answer != HandoffContinued {
		t.Fatalf("continued не завершил handoff: %+v", st)
	}
	// inconclusive и not-continued не скрыты: все три ответа в журнале.
	want := []HandoffAnswer{HandoffInconclusive, HandoffNotContinued, HandoffContinued}
	if len(st.Answers) != len(want) {
		t.Fatalf("ответов %d, ждали %d: %+v", len(st.Answers), len(want), st.Answers)
	}
	for i, a := range want {
		if st.Answers[i].Answer != a {
			t.Fatalf("ответ %d = %q, ждали %q", i, st.Answers[i].Answer, a)
		}
	}
}

func TestRecordHandoffAnswerValidatesAndIsolatesTasks(t *testing.T) {
	e := newEnv(t)
	for _, a := range []HandoffAnswer{"", "maybe", "CONTINUED", "continued "} {
		if _, err := e.svc.RecordHandoffAnswer("T3.157", a, time.Time{}); taskstore.Code(err) != 1 {
			t.Errorf("ответ %q: код %d, ждали 1", a, taskstore.Code(err))
		}
	}
	if _, err := e.svc.RecordHandoffAnswer("  ", HandoffContinued, time.Time{}); taskstore.Code(err) != 1 {
		t.Fatalf("пустая задача: код %d, ждали 1", taskstore.Code(err))
	}
	if _, err := e.svc.HandoffStatus("  "); taskstore.Code(err) != 1 {
		t.Fatalf("HandoffStatus с пустой задачей: %v", err)
	}
	st, err := e.svc.HandoffStatus("T3.157")
	if err != nil || len(st.Answers) != 0 {
		t.Fatalf("неверные ответы записаны: %+v err=%v", st, err)
	}

	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if _, err := e.svc.RecordHandoffAnswer("T3.157", HandoffContinued, at); err != nil {
		t.Fatal(err)
	}
	other, err := e.svc.HandoffStatus("T3.158")
	if err != nil {
		t.Fatal(err)
	}
	if other.Completed || len(other.Answers) != 0 {
		t.Fatalf("ответ по другой задаче виден: %+v", other)
	}
	fresh := NewService(e.cfg)
	got, err := fresh.HandoffStatus("T3.157")
	if err != nil || !got.Completed || len(got.Answers) != 1 {
		t.Fatalf("новый сервис: %+v err=%v", got, err)
	}
}

func TestConcurrentHandoffAnswersAreAllKept(t *testing.T) {
	e := newEnv(t)
	const n = 12
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := e.svc.RecordHandoffAnswer("T3.157", HandoffInconclusive,
				time.Date(2026, 11, 20, 12, 0, i, 0, time.UTC))
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	st, err := e.svc.HandoffStatus("T3.157")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Answers) != n {
		t.Fatalf("записано %d ответов, ждали %d", len(st.Answers), n)
	}
}

func reaperTestTime() time.Time {
	return time.Date(2026, 11, 20, 14, 10, 0, 0, time.UTC)
}

func cardByID(t *testing.T, e *env, id string) Card {
	t.Helper()
	cards, err := LoadCards(e.tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("карточка %s не найдена", id)
	return Card{}
}

func TestReapStaleCardsKeepsLiveWatch(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.10", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-19T14:03:21Z")

	got, err := e.svc.ReapStaleCards(ReapOptions{
		Now:        reaperTestTime(),
		Agent:      "reaper-test",
		WatchAlive: func(cardID string) (bool, error) { return cardID == "T2.10", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 1 || len(got.Reaped) != 0 || len(got.Skipped) != 1 || got.Skipped[0] != "T2.10" {
		t.Fatalf("результат прохода: %+v", got)
	}
	if c := cardByID(t, e, "T2.10"); c.Status != "in_progress" || c.Agent != "agent-1" {
		t.Fatalf("живая карточка изменена: %+v", c)
	}
	log, err := e.svc.ReaperLog()
	if err != nil || len(log) != 0 {
		t.Fatalf("журнал для живой карточки: %+v err=%v", log, err)
	}
}

func TestReapStaleCardsFreesDeadWorker(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.11", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-19T14:03:21Z")

	got, err := e.svc.ReapStaleCards(ReapOptions{Now: reaperTestTime(), Agent: "reaper-test"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 1 || len(got.Reaped) != 1 || got.Reaped[0] != "T2.11" || len(got.Skipped) != 0 {
		t.Fatalf("результат прохода: %+v", got)
	}
	c := cardByID(t, e, "T2.11")
	if c.Status != "todo" || c.Agent != "нет" || c.Time != "нет" {
		t.Fatalf("карточка не освобождена: %+v", c)
	}
	log, err := e.svc.ReaperLog()
	if err != nil || len(log) != 1 || log[0].CardID != "T2.11" || log[0].Agent != "reaper-test" {
		t.Fatalf("журнал: %+v err=%v", log, err)
	}
	if _, err := e.svc.Take(TakeOptions{
		ID: "T2.11", Agent: "agent-2", Time: "2026-11-20T14:11:00Z",
	}); err != nil {
		t.Fatalf("освобождённую карточку нельзя взять повторно: %v", err)
	}
}

func TestReapStaleCardsIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.12", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-19T14:03:21Z")

	first, err := e.svc.ReapStaleCards(ReapOptions{Now: reaperTestTime(), Agent: "reaper-test"})
	if err != nil || len(first.Reaped) != 1 {
		t.Fatalf("первый проход: %+v err=%v", first, err)
	}
	second, err := e.svc.ReapStaleCards(ReapOptions{Now: reaperTestTime(), Agent: "reaper-test"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Scanned != 0 || len(second.Reaped) != 0 {
		t.Fatalf("второй проход изменил карточку: %+v", second)
	}
	log, err := e.svc.ReaperLog()
	if err != nil || len(log) != 1 {
		t.Fatalf("журнал после двух проходов: %+v err=%v", log, err)
	}
}

func TestReapStaleCardsSkipsLiveProcessAndFreshCard(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.13", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-19T14:03:21Z")
	e.card(t, "T2.14", "in_progress", "task", "ADR 0001", "internal/app/a/two.go",
		"agent-2", "2026-11-20T13:55:00Z")

	got, err := e.svc.ReapStaleCards(ReapOptions{
		Now:          reaperTestTime(),
		Agent:        "reaper-test",
		ProcessAlive: func(cardID string) (bool, error) { return cardID == "T2.13", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 2 || len(got.Reaped) != 0 || len(got.Skipped) != 2 {
		t.Fatalf("результат прохода: %+v", got)
	}
	for _, id := range []string{"T2.13", "T2.14"} {
		if c := cardByID(t, e, id); c.Status != "in_progress" {
			t.Fatalf("карточка %s изменена: %+v", id, c)
		}
	}
}

func TestReapStaleCardsHonorsInjectedTTL(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.15", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-20T13:55:00Z")

	got, err := e.svc.ReapStaleCards(ReapOptions{
		Now: reaperTestTime(), TTL: 5 * time.Minute, Agent: "reaper-test",
	})
	if err != nil || len(got.Reaped) != 1 || got.Reaped[0] != "T2.15" {
		t.Fatalf("внедрённый TTL проигнорирован: %+v err=%v", got, err)
	}
}

// deferredCard создаёт отложенную карточку с указанным значением поля даты.
func (e *env) deferredCard(t *testing.T, id, until string) string {
	t.Helper()
	e.card(t, id, "blocked", "task", "ADR 0001", "internal/app/a/one.go", "нет", "нет")
	path := filepath.Join(e.tasksDir, id+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "Статус: blocked\n",
		"Статус: blocked\n"+deferUntilField+": "+until+"\n", 1)
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func deferWakeEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	e := newEnv(t)
	cfg := e.cfg
	cfg.DeferWakeNow = func() time.Time { return now }
	e.cfg = cfg
	e.svc = NewService(cfg)
	return e
}

func TestDeferWakeWakesExpiredCardOnReadyRead(t *testing.T) {
	e := deferWakeEnv(t, time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	e.deferredCard(t, "T2.30", "2026-11-20T10:00:00Z")

	if _, err := e.svc.List(ListOptions{}); err != nil {
		t.Fatal(err)
	}
	c := cardByID(t, e, "T2.30")
	if c.Status != "todo" || strings.TrimSpace(c.Extra[deferUntilField]) != "" {
		t.Fatalf("карточка не проснулась или дата не очищена: %+v", c)
	}
	log, err := e.svc.DeferWakeLog()
	if err != nil || len(log) != 1 || log[0].CardID != "T2.30" || log[0].Action != "wake" {
		t.Fatalf("журнал пробуждения: %+v err=%v", log, err)
	}
}

func TestDeferWakeSkipsCardTakenBetweenSnapshotAndWrite(t *testing.T) {
	e := deferWakeEnv(t, time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	e.deferredCard(t, "T2.31", "2026-11-20T10:00:00Z")
	second := e.deferredCard(t, "T2.32", "2026-11-20T10:00:00Z")

	cfg := e.cfg
	cfg.DeferWakeReadCard = func(path string) ([]byte, error) {
		if path != second {
			return os.ReadFile(path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		taken := strings.Replace(string(raw), "Статус: blocked", "Статус: in_progress", 1)
		if err := os.WriteFile(path, []byte(taken), 0644); err != nil {
			return nil, err
		}
		return []byte(taken), nil
	}
	e.cfg = cfg
	e.svc = NewService(cfg)

	rep, err := e.svc.SweepExpiredDefers(DeferWakeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 2 || len(rep.Woken) != 1 || rep.Woken[0] != "T2.31" {
		t.Fatalf("результат прохода: %+v", rep)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "T2.32" {
		t.Fatalf("изменённая карточка не пропущена: %+v", rep)
	}
	if c := cardByID(t, e, "T2.32"); c.Status != "in_progress" {
		t.Fatalf("карточка, взятая конкурентом, изменена: %+v", c)
	}
}

func TestDeferWakeLeavesIndefiniteDeferral(t *testing.T) {
	e := deferWakeEnv(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	e.deferredCard(t, "T2.33", deferForever)

	for i := 0; i < 10; i++ {
		rep, err := e.svc.SweepExpiredDefers(DeferWakeOptions{})
		if err != nil || rep.Scanned != 0 || len(rep.Woken) != 0 {
			t.Fatalf("проход %d: %+v err=%v", i, rep, err)
		}
	}
	c := cardByID(t, e, "T2.33")
	if c.Status != "blocked" || strings.TrimSpace(c.Extra[deferUntilField]) != deferForever {
		t.Fatalf("бессрочная отложка изменена: %+v", c)
	}
}

func TestDeferWakeWriteErrorKeepsListAndWarnsOnce(t *testing.T) {
	e := deferWakeEnv(t, time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	e.deferredCard(t, "T2.34", "2026-11-20T10:00:00Z")

	cfg := e.cfg
	cfg.DeferWakeWriteCard = func(path string, data []byte) error {
		return fmt.Errorf("искусственная ошибка записи")
	}
	e.cfg = cfg
	e.svc = NewService(cfg)

	out, err := e.svc.List(ListOptions{})
	if err != nil {
		t.Fatalf("ошибка записи уронила ready-чтение: %v", err)
	}
	if !strings.Contains(out, "T2.34") {
		t.Fatalf("список потерян: %s", out)
	}
	if strings.Count(out, "предупреждение:") != 1 {
		t.Fatalf("ожидалась одна строка предупреждения: %s", out)
	}
	if c := cardByID(t, e, "T2.34"); c.Status != "blocked" {
		t.Fatalf("карточка изменена при ошибке записи: %+v", c)
	}
	log, err := e.svc.DeferWakeLog()
	if err != nil || len(log) != 1 || log[0].Action != "error" {
		t.Fatalf("ошибка не записана в журнал: %+v err=%v", log, err)
	}
}

func TestDeferWakeSecondPassDoesNotChangeFiles(t *testing.T) {
	e := deferWakeEnv(t, time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC))
	path := e.deferredCard(t, "T2.35", "2026-11-20T10:00:00Z")

	if _, err := e.svc.SweepExpiredDefers(DeferWakeOptions{}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := e.svc.SweepExpiredDefers(DeferWakeOptions{})
	if err != nil || rep.Scanned != 0 || len(rep.Woken) != 0 {
		t.Fatalf("второй проход: %+v err=%v", rep, err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("второй проход изменил файл:\n%s\n%s", first, second)
	}
}

func TestDeferWritesDateAndStatus(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.36", "todo", "task", "ADR 0001", "internal/app/a/one.go", "нет", "нет")

	if _, err := e.svc.Defer(DeferOptions{ID: "T2.36", Until: "2026-11-30"}); err != nil {
		t.Fatal(err)
	}
	c := cardByID(t, e, "T2.36")
	if c.Status != "blocked" || strings.TrimSpace(c.Extra[deferUntilField]) != "2026-11-30T00:00:00Z" {
		t.Fatalf("дата отложки не записана: %+v", c)
	}
	if _, err := e.svc.Defer(DeferOptions{ID: "T2.36", Forever: true}); err != nil {
		t.Fatal(err)
	}
	c = cardByID(t, e, "T2.36")
	if strings.TrimSpace(c.Extra[deferUntilField]) != deferForever {
		t.Fatalf("бессрочная отложка не записана: %+v", c)
	}
}

func reclaimField(t *testing.T, err error) string {
	t.Helper()
	var fe *ReclaimFieldError
	if !errors.As(err, &fe) {
		t.Fatalf("ожидалась ReclaimFieldError, получено %T: %v", err, err)
	}
	return fe.Field
}

func TestReclaimRejectsOverCapBeforeRead(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.20", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-19T14:03:21Z")
	reads := 0
	e.svc.loadCards = func(dir string) ([]Card, error) {
		reads++
		return LoadCards(dir)
	}
	ids := make([]string, 0, MaxReclaimIDs+1)
	for i := 0; i <= MaxReclaimIDs; i++ {
		ids = append(ids, fmt.Sprintf("T2.%d", i))
	}
	got, err := e.svc.ReapStaleCards(ReapOptions{
		Now: reaperTestTime(), Agent: "reaper-test", IDs: ids,
	})
	if err == nil || reclaimField(t, err) != "ids" {
		t.Fatalf("ожидалась ошибка поля ids, получено %+v err=%v", got, err)
	}
	if reads != 0 {
		t.Fatalf("карточки прочитаны до валидации: %d", reads)
	}
}

func TestReclaimFieldErrorsNameFields(t *testing.T) {
	cases := []struct {
		name string
		req  ReclaimRequest
		want string
	}{
		{"empty agent", ReclaimRequest{Agent: "   "}, "agent"},
		{"empty id item", ReclaimRequest{Agent: "reaper-test", IDs: []string{"T2.1", "  "}}, "ids"},
		{"negative age", ReclaimRequest{Agent: "reaper-test", OlderThan: -time.Second}, "older_than"},
		{"long agent", ReclaimRequest{Agent: strings.Repeat("a", MaxReclaimAgentBytes+1)}, "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReclaimRequest(tc.req)
			if err == nil || reclaimField(t, err) != tc.want {
				t.Fatalf("поле ошибки: хотим %q, получили err=%v", tc.want, err)
			}
		})
	}
	if err := ValidateReclaimRequest(ReclaimRequest{Agent: strings.Repeat("a", MaxReclaimAgentBytes)}); err != nil {
		t.Fatalf("исполнитель длиной %d байт должен проходить: %v", MaxReclaimAgentBytes, err)
	}
}

func TestReclaimSharedCutoffAcrossPaths(t *testing.T) {
	req := ReclaimRequest{
		Agent:     "reaper-test",
		OlderThan: 5 * time.Minute,
		IDs:       []string{"T2.1", "T2.2", "T2.1"},
	}
	wantCutoff := reaperTestTime().Add(-5 * time.Minute)
	// Три вызова моделируют один запрос из CLI, MCP и daemon.
	for i := 0; i < 3; i++ {
		plan, err := prepareReclaim(req, reaperTestTime())
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Cutoff.Equal(wantCutoff) {
			t.Fatalf("путь %d: срез %s, хотим %s", i, plan.Cutoff, wantCutoff)
		}
		if len(plan.IDs) != 2 || !plan.wants("T2.1") || !plan.wants("T2.2") || plan.wants("T2.3") {
			t.Fatalf("путь %d: набор ID %+v", i, plan.IDs)
		}
	}
}

func TestReapStaleCardsHonorsIDFilter(t *testing.T) {
	e := newEnv(t)
	e.card(t, "T2.21", "in_progress", "task", "ADR 0001", "internal/app/a/one.go",
		"agent-1", "2026-11-19T14:03:21Z")
	e.card(t, "T2.22", "in_progress", "task", "ADR 0001", "internal/app/a/two.go",
		"agent-2", "2026-11-19T14:03:21Z")

	got, err := e.svc.ReapStaleCards(ReapOptions{
		Now: reaperTestTime(), Agent: "reaper-test",
		IDs: []string{"T2.21", "T2.21"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 1 || len(got.Reaped) != 1 || got.Reaped[0] != "T2.21" {
		t.Fatalf("фильтр ID проигнорирован: %+v", got)
	}
	if c := cardByID(t, e, "T2.22"); c.Status != "in_progress" {
		t.Fatalf("карточка вне фильтра изменена: %+v", c)
	}
	log, err := e.svc.ReaperLog()
	if err != nil || len(log) != 1 || log[0].CardID != "T2.21" {
		t.Fatalf("журнал: %+v err=%v", log, err)
	}
}

func TestReapStaleCardsRejectsLongAgentBeforeJournal(t *testing.T) {
	e := newEnv(t)
	reads := 0
	e.svc.loadCards = func(dir string) ([]Card, error) {
		reads++
		return LoadCards(dir)
	}
	got, err := e.svc.ReapStaleCards(ReapOptions{
		Now: reaperTestTime(), Agent: strings.Repeat("a", MaxReclaimAgentBytes+1),
	})
	if err == nil || reclaimField(t, err) != "agent" {
		t.Fatalf("ожидалась ошибка поля agent, получено %+v err=%v", got, err)
	}
	if reads != 0 {
		t.Fatalf("карточки прочитаны до валидации: %d", reads)
	}
	log, err := e.svc.ReaperLog()
	if err != nil || len(log) != 0 {
		t.Fatalf("журнал записан при неверном запросе: %+v err=%v", log, err)
	}
}

func writeHook(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func withHooks(e *env) string {
	dir := filepath.Join(e.cfg.Root, "hooks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		panic(err)
	}
	e.cfg.HooksDir = dir
	e.svc = NewService(e.cfg)
	return dir
}

func TestHooksClaimedFiresOnceAfterTake(t *testing.T) {
	e := newEnv(t)
	dir := withHooks(e)
	payload := filepath.Join(dir, "payload.json")
	writeHook(t, dir, "claimed.sh", "#!/bin/sh\ncat > "+payload+"\n")
	e.card(t, "T2.30", "todo", "task", "ADR 0001", "internal/app/a/one.go", "нет", "нет")

	out, err := e.svc.Take(TakeOptions{ID: "T2.30", Agent: "agent-1", Time: "2026-11-22T10:00:00Z"})
	if err != nil || !strings.Contains(out, "in_progress") {
		t.Fatalf("take: %q err=%v", out, err)
	}
	if c := cardByID(t, e, "T2.30"); c.Status != "in_progress" {
		t.Fatalf("статус карточки: %q", c.Status)
	}

	raw, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got hookPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("载荷 хука: %v\n%s", err, raw)
	}
	if got.Event != "claimed" || got.CardID != "T2.30" || got.Time != "2026-11-22T10:00:00Z" {
		t.Fatalf("载荷: %+v", got)
	}

	entries, err := e.svc.HookLog()
	if err != nil || len(entries) != 1 {
		t.Fatalf("журнал: %+v err=%v", entries, err)
	}
	if entries[0].Event != "claimed" || entries[0].CardID != "T2.30" || len(entries[0].Hooks) != 1 || entries[0].Error != "" {
		t.Fatalf("запись: %+v", entries[0])
	}
}

func TestHooksUnblockedAndClosedEachWriteOneEntry(t *testing.T) {
	e := newEnv(t)
	dir := withHooks(e)
	writeHook(t, dir, "unblocked.sh", "#!/bin/sh\nexit 0\n")
	writeHook(t, dir, "closed.sh", "#!/bin/sh\nexit 0\n")

	for _, event := range []HookEvent{HookUnblocked, HookClosed} {
		if err := e.svc.RunHooks(event, "T2.31", "2026-11-22T11:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := e.svc.HookLog()
	if err != nil || len(entries) != 2 {
		t.Fatalf("журнал: %+v err=%v", entries, err)
	}
	for i, want := range []string{"unblocked", "closed"} {
		if entries[i].Event != want || entries[i].CardID != "T2.31" || len(entries[i].Hooks) != 1 {
			t.Fatalf("запись %d: %+v", i, entries[i])
		}
	}
}

func TestFailingHookDoesNotChangeCard(t *testing.T) {
	e := newEnv(t)
	dir := withHooks(e)
	writeHook(t, dir, "claimed.sh", "#!/bin/sh\necho boom >&2\nexit 1\n")
	e.card(t, "T2.32", "todo", "task", "ADR 0001", "internal/app/a/one.go", "нет", "нет")

	out, err := e.svc.Take(TakeOptions{ID: "T2.32", Agent: "agent-1", Time: "2026-11-22T12:00:00Z"})
	if err != nil || !strings.Contains(out, "in_progress") {
		t.Fatalf("take: %q err=%v", out, err)
	}
	if c := cardByID(t, e, "T2.32"); c.Status != "in_progress" {
		t.Fatalf("статус карточки: %q", c.Status)
	}
	entries, err := e.svc.HookLog()
	if err != nil || len(entries) != 1 {
		t.Fatalf("журнал: %+v err=%v", entries, err)
	}
	if !strings.Contains(entries[0].Error, "boom") {
		t.Fatalf("ошибка хука не записана: %+v", entries[0])
	}
}

func TestHooksRunInSortedOrder(t *testing.T) {
	e := newEnv(t)
	dir := withHooks(e)
	order := filepath.Join(dir, "order.txt")
	writeHook(t, dir, "claimed.20.sh", "#!/bin/sh\necho claimed.20 >> "+order+"\n")
	writeHook(t, dir, "claimed.10.sh", "#!/bin/sh\necho claimed.10 >> "+order+"\n")
	e.card(t, "T2.33", "todo", "task", "ADR 0001", "internal/app/a/one.go", "нет", "нет")

	if _, err := e.svc.Take(TakeOptions{ID: "T2.33", Agent: "agent-1", Time: "2026-11-22T13:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "claimed.10\nclaimed.20\n" {
		t.Fatalf("порядок: %q", raw)
	}
	entries, err := e.svc.HookLog()
	if err != nil || len(entries) != 1 {
		t.Fatalf("журнал: %+v err=%v", entries, err)
	}
	if !reflect.DeepEqual(entries[0].Hooks, []string{"claimed.10.sh", "claimed.20.sh"}) {
		t.Fatalf("список хуков: %+v", entries[0].Hooks)
	}
}

func TestHooksDisabledWithoutDir(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.RunHooks(HookClaimed, "T2.34", "2026-11-22T14:00:00Z"); err != nil {
		t.Fatal(err)
	}
	entries, err := e.svc.HookLog()
	if err != nil || len(entries) != 0 {
		t.Fatalf("журнал без каталога хуков: %+v err=%v", entries, err)
	}
}

// ---- CBX-01: post-commit recheck of dependent blocked flags ----

func (e *env) depCard(t *testing.T, id, status, deps string) {
	t.Helper()
	text := fmt.Sprintf("# %s. Тест\nСтатус: %s\nФаза: P2\nТип: task\n"+
		"Владелец файлов: internal/app/%s.go\nЗависимости: %s\nСсылка: ADR 0001\n"+
		"Агент: нет\nВремя: нет\nЦель: ц\nКонтекст: к\nШаги: ш\nПроверка: п\nПриёмка: пр\n",
		id, status, strings.ToLower(id), deps)
	if err := os.WriteFile(filepath.Join(e.tasksDir, id+".md"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) markDone(t *testing.T, id string) {
	t.Helper()
	c := cardByID(t, e, id)
	updated := strings.Replace(c.Raw, "Статус: "+c.Status, "Статус: done", 1)
	if err := os.WriteFile(c.Path, []byte(updated), 0644); err != nil {
		t.Fatal(err)
	}
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestRecheckBlockedConvergesAfterParallelClosers(t *testing.T) {
	e := newEnv(t)
	e.depCard(t, "T9.1", "todo", "нет")
	e.depCard(t, "T9.2", "todo", "нет")
	e.depCard(t, "T9.3", "blocked", "T9.1, T9.2")

	// Оба блокировщика публикуются до того, как recheck читает граф.
	e.markDone(t, "T9.1")
	e.markDone(t, "T9.2")

	rep := e.svc.RecheckBlocked([]string{"T9.1", "T9.2"}, reaperTestTime())
	if !hasID(rep.Changed, "T9.3") || len(rep.Failed) != 0 {
		t.Fatalf("отчёт: %+v", rep)
	}
	if c := cardByID(t, e, "T9.3"); c.Status != "todo" {
		t.Fatalf("зависимая карточка осталась %q, ждали todo", c.Status)
	}
	cards, err := LoadCards(e.tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if stale := taskstore.StaleBlocked(cards); len(stale) != 0 {
		t.Fatalf("карточки с устаревшим флагом: %v", stale)
	}
}

func TestRecheckBlockedTwoClosersReturnTogether(t *testing.T) {
	e := newEnv(t)
	e.depCard(t, "T9.14", "todo", "нет")
	e.depCard(t, "T9.15", "todo", "нет")
	e.depCard(t, "T9.16", "blocked", "T9.14, T9.15")

	e.markDone(t, "T9.14")
	e.markDone(t, "T9.15")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range []string{"T9.14", "T9.15"} {
		wg.Add(1)
		go func(blocker string) {
			defer wg.Done()
			<-start
			e.svc.RecheckBlocked([]string{blocker}, reaperTestTime())
		}(id)
	}
	close(start)
	wg.Wait()

	if c := cardByID(t, e, "T9.16"); c.Status != "todo" {
		t.Fatalf("зависимая карточка %q, ждали todo", c.Status)
	}
	cards, err := LoadCards(e.tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if stale := taskstore.StaleBlocked(cards); len(stale) != 0 {
		t.Fatalf("карточки с устаревшим флагом: %v", stale)
	}
}

func TestRecheckBlockedIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.depCard(t, "T9.4", "done", "нет")
	e.depCard(t, "T9.5", "blocked", "T9.4")

	first := e.svc.RecheckBlocked([]string{"T9.4"}, reaperTestTime())
	if !hasID(first.Changed, "T9.5") {
		t.Fatalf("первый проход ничего не изменил: %+v", first)
	}
	path := cardByID(t, e, "T9.5").Path
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second := e.svc.RecheckBlocked([]string{"T9.4"}, reaperTestTime())
	if len(second.Changed) != 0 || len(second.Failed) != 0 {
		t.Fatalf("повторный проход изменил файлы: %+v", second)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("повторный проход перезаписал файл: %q -> %q", before, after)
	}
}

func TestRecheckBlockedSetsFlagWhenBlockerAlive(t *testing.T) {
	e := newEnv(t)
	e.depCard(t, "T9.6", "todo", "нет")
	e.depCard(t, "T9.7", "todo", "T9.6")

	rep := e.svc.RecheckBlocked([]string{"T9.6"}, reaperTestTime())
	if !hasID(rep.Changed, "T9.7") {
		t.Fatalf("живой блокировщик не поднял флаг: %+v", rep)
	}
	if c := cardByID(t, e, "T9.7"); c.Status != "blocked" {
		t.Fatalf("статус карточки %q, ждали blocked", c.Status)
	}
}

func TestRecheckReportCountsFiftyPasses(t *testing.T) {
	e := newEnv(t)
	e.depCard(t, "T9.8", "done", "нет")
	e.depCard(t, "T9.9", "blocked", "T9.8")

	for i := 0; i < 50; i++ {
		rep := e.svc.RecheckBlocked([]string{"T9.8"}, reaperTestTime())
		if len(rep.Failed) != 0 {
			t.Fatalf("проход %d дал ошибку записи: %+v", i, rep)
		}
	}
	entries, err := e.svc.RecheckLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 50 {
		t.Fatalf("записей в отчёте %d, ждали 50", len(entries))
	}
	count, err := e.svc.RecheckCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Fatalf("счётчик %d, ждали 50", count)
	}
}

func TestRecheckWriteFailureIsReportedNotReturned(t *testing.T) {
	e := newEnv(t)
	e.depCard(t, "T9.10", "done", "нет")
	e.depCard(t, "T9.11", "blocked", "T9.10")
	failPath := cardByID(t, e, "T9.11").Path
	e.cfg.RecheckWriteCard = func(path string, data []byte) error {
		if path == failPath {
			return fmt.Errorf("искусственная ошибка архива")
		}
		return taskstore.AtomicWrite(path, data, 0644)
	}
	e.svc = NewService(e.cfg)

	rep := e.svc.RecheckBlocked([]string{"T9.10"}, reaperTestTime())
	if !hasID(rep.Failed, "T9.11") || len(rep.Changed) != 0 {
		t.Fatalf("отчёт об ошибке: %+v", rep)
	}
	entries, err := e.svc.RecheckLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !hasID(entries[0].Failed, "T9.11") {
		t.Fatalf("журнал: %+v", entries)
	}
	if c := cardByID(t, e, "T9.11"); c.Status != "blocked" {
		t.Fatalf("карточка изменилась при ошибке записи: %q", c.Status)
	}
}

func TestRecheckDoesNotChangeHookBehavior(t *testing.T) {
	e := newEnv(t)
	dir := withHooks(e)
	writeHook(t, dir, "unblocked.sh", "#!/bin/sh\nexit 0\n")
	e.depCard(t, "T9.12", "done", "нет")
	e.depCard(t, "T9.13", "blocked", "T9.12")

	e.svc.RecheckBlocked([]string{"T9.12"}, reaperTestTime())
	entries, err := e.svc.HookLog()
	if err != nil || len(entries) != 0 {
		t.Fatalf("recheck изменил журнал хуков: %+v err=%v", entries, err)
	}
}

// ---- ADR 0041 п6 / HD-022: журнал precheck и связь коммита ----

func TestRecordPrecheckFailureRejectsEmptyReason(t *testing.T) {
	e := newEnv(t)
	for _, reason := range []string{"", "   ", "\t\n"} {
		if err := e.svc.RecordPrecheckFailure(reason, time.Time{}); taskstore.Code(err) != 1 {
			t.Errorf("причина %q: код %d, err=%v", reason, taskstore.Code(err), err)
		}
	}
	log, err := e.svc.PrecheckLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 0 {
		t.Fatalf("отклонённая причина попала в журнал: %+v", log)
	}
}

func TestRecordPrecheckFailureWritesUTCEntry(t *testing.T) {
	e := newEnv(t)
	local := time.FixedZone("test", 3*60*60)
	at := time.Date(2026, 11, 20, 15, 0, 0, 0, local)
	want := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := e.svc.RecordPrecheckFailure("  проверка не прошла  ", at); err != nil {
		t.Fatal(err)
	}
	log, err := e.svc.PrecheckLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Fatalf("в журнале %d записей, ждали 1", len(log))
	}
	got := log[0]
	if got.Outcome != "precheck_failure" || got.Reason != "проверка не прошла" {
		t.Errorf("запись: %+v", got)
	}
	if !got.Time.Equal(want) || got.Time.Location() != time.UTC {
		t.Errorf("время %v (%v), ждали %v UTC", got.Time, got.Time.Location(), want)
	}
}

func TestPrecheckLogReturnsEntriesInOrderAndCopiesSlice(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	reasons := []string{"первая причина", "вторая причина", "третья причина"}
	for _, reason := range reasons {
		if err := e.svc.RecordPrecheckFailure(reason, at); err != nil {
			t.Fatal(err)
		}
	}
	log, err := e.svc.PrecheckLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != len(reasons) {
		t.Fatalf("в журнале %d записей, ждали %d", len(log), len(reasons))
	}
	for i, reason := range reasons {
		if log[i].Reason != reason {
			t.Errorf("запись %d: причина %q, ждали %q", i, log[i].Reason, reason)
		}
	}

	log[0].Reason = "подмена"
	log[0].Outcome = "подмена"
	log = append(log, PrecheckLogEntry{Reason: "лишняя"})

	again, err := e.svc.PrecheckLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(reasons) || again[0].Reason != reasons[0] || again[0].Outcome != "precheck_failure" {
		t.Fatalf("изменение копии затронуло хранилище: %+v", again)
	}
}

func TestCheckCommitLinkageDelegatesForFeatAndFix(t *testing.T) {
	e := newEnv(t)
	ports := LinkagePorts{
		ValidADR:  func(ref string) bool { return ref == "ADR 0041" },
		ValidCard: func(ref string) bool { return ref == "TG-06" },
	}
	withRefs := "Ссылка на ADR 0041 и карточка TG-06."
	cases := []struct {
		name    string
		message string
		ports   LinkagePorts
		want    LinkageVerdict
	}{
		{"chore", "chore: обновить README", ports, LinkageEmpty},
		{"без типа коммита", "обновление документации", ports, LinkageEmpty},
		{"feat с валидными ссылками", "feat(taskcli): журнал precheck\n\n" + withRefs, ports, taskstore.LinkageLinked},
		{"fix с валидными ссылками", "fix(taskcli): вернуть копию журнала\n\n" + withRefs, ports, taskstore.LinkageLinked},
		{
			"feat с недействительной ссылкой",
			"feat(taskcli): журнал precheck\n\n" + withRefs,
			LinkagePorts{
				ValidADR:  func(ref string) bool { return false },
				ValidCard: func(ref string) bool { return true },
			},
			taskstore.LinkageMissing,
		},
		{
			"feat с обоснованием",
			"feat(taskcli): без карточки\n\nОбоснование: внешний инструмент",
			ports,
			taskstore.LinkageExempted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.svc.CheckCommitLinkage(tc.message, tc.ports); got != tc.want {
				t.Errorf("вердикт %q, ждали %q", got, tc.want)
			}
		})
	}
}
