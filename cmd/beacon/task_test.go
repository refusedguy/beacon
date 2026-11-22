package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/app/taskcli"
	"github.com/refusedguy/beacon/internal/infra/server"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

func realRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testConfig(root string) taskcli.Config {
	return taskcli.Config{
		TasksDir: filepath.Join(root, "docs", "tasks"),
		ADRDir:   filepath.Join(root, "docs", "adrs"),
		StateDir: filepath.Join(root, "state-test"),
		Root:     root,
	}
}

func TestTaskCommandListRealCards(t *testing.T) {
	root := realRoot(t)
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "list", "--format", "json"}, &stdout, &stderr, testConfig(root))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("неверный JSON: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			want++
		}
	}
	if len(rows) != want {
		t.Fatalf("в JSON %d карточек, на диске %d", len(rows), want)
	}
	if len(rows) < 52 {
		t.Fatalf("ожидались все карточки, получено %d", len(rows))
	}
	// New waves add S1.001 and UI-01 cards, which sort before T0.1. The
	// contract is that every disk card is listed, not a fixed first id.
	found := false
	for _, row := range rows {
		if row["id"] == "T0.1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("карточка T0.1 отсутствует в выводе")
	}
}

func TestTaskCommandExitCodesAndStreams(t *testing.T) {
	root := realRoot(t)
	cfg := testConfig(root)
	cases := []struct {
		name string
		argv []string
		code int
	}{
		{"нет подкоманды", []string{"task"}, 1},
		{"неизвестная подкоманда", []string{"task", "frobnicate"}, 1},
		{"неизвестный флаг", []string{"task", "list", "--bogus"}, 1},
		{"неверный формат", []string{"task", "list", "--format", "yaml"}, 1},
		{"неверный статус", []string{"task", "list", "--status", "open"}, 1},
		{"new без флагов", []string{"task", "new"}, 1},
		{"take без агента", []string{"task", "take", "T2.1"}, 1},
		{"take без ID", []string{"task", "take"}, 1},
		{"take с двумя ID", []string{"task", "take", "T2.1", "T2.2"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, cfg)
			if code != tc.code {
				t.Errorf("код = %d, ждали %d", code, tc.code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout не пустой при ошибке: %q", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Errorf("пустой stderr при ошибке")
			}
		})
	}
}

func TestTaskCommandNewDryRunTakeAndSelfTest(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "docs", "tasks")
	adrDir := filepath.Join(root, "docs", "adrs")
	for _, d := range []string{tasksDir, adrDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(adrDir, "0001-accepted.md"), []byte("Статус: accepted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := taskcli.Config{
		TasksDir: tasksDir, ADRDir: adrDir,
		StateDir: filepath.Join(root, "state"), Root: root,
	}
	newArgs := []string{"task", "new", "--phase", "P2", "--title", "Командная карточка",
		"--reference", "ADR 0001", "--owner-files", "internal/app/cmd/one.go"}
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig(append([]string{}, append(newArgs, "--dry-run")...), &stdout, &stderr, cfg)
	if code != 0 || !strings.HasPrefix(stdout.String(), "T2.1 ") {
		t.Fatalf("dry-run: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(tasksDir, "T2.1.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run создал файл")
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig(newArgs, &stdout, &stderr, cfg)
	if code != 0 || !strings.HasPrefix(stdout.String(), "T2.1 ") {
		t.Fatalf("new: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "take", "T2.1", "--agent", "cli-agent",
		"--time", "2026-11-19T14:03:21Z"}, &stdout, &stderr, cfg)
	if code != 0 || strings.TrimSpace(stdout.String()) != "T2.1 in_progress cli-agent 2026-11-19T14:03:21Z" {
		t.Fatalf("take: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "take", "T2.1", "--agent", "other"}, &stdout, &stderr, cfg)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "cli-agent") {
		t.Fatalf("второй take: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "--self-test"}, &stdout, &stderr, cfg)
	if code != 0 || !strings.Contains(stdout.String(), "self-test passed") {
		t.Fatalf("self-test: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

// writeWhyCard пишет карточку с обязательными полями для тестов why.
func writeWhyCard(t *testing.T, dir, id, status, agent, deps, context string) {
	t.Helper()
	body := fmt.Sprintf(`# %s. Карточка %s

Тип: task
Статус: %s
Фаза: P1
Владелец файлов: cmd/%s.go
Зависимости: %s
Ссылка: ADR 0001
Цель: Проверить цепочку
Контекст: %s
Шаги: Шаг
Проверка: Проверка
Приёмка: Приёмка
Агент: %s
Время: 2026-11-20T00:00:00Z
`, id, id, status, strings.ReplaceAll(id, ".", "-"), deps, context, agent)
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func whyConfig(dir string) taskcli.Config {
	return taskcli.Config{TasksDir: dir, ADRDir: dir, StateDir: dir, Root: dir}
}

func TestTaskCommandWhyChain(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T1.1", "todo", "", "T1.2", "цепочка")
	writeWhyCard(t, dir, "T1.2", "in_progress", "alice", "T1.3", "цепочка")
	writeWhyCard(t, dir, "T1.3", "todo", "", "нет", "цепочка")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T1.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"T1.1", "T1.2", "depends-on", "alice", "in_progress", "T1.3", "todo"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q: %q", want, out)
		}
	}
	if !strings.Contains(out, "1. T1.2 через depends-on") || !strings.Contains(out, "2. T1.3 через depends-on") {
		t.Errorf("неверный порядок шагов: %q", out)
	}

	// Команда принимает и форму без префикса task.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"why", "T1.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || !strings.Contains(stdout.String(), "T1.2 через depends-on") {
		t.Fatalf("alias why: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestTaskCommandWhyEdgeKinds(t *testing.T) {
	dir := t.TempDir()
	edges := []struct{ from, to, kind string }{
		{"T2.1", "T2.2", "depends-on"},
		{"T2.2", "T2.3", "conflicts-with"},
		{"T2.3", "T2.4", "part-of"},
		{"T2.4", "T2.5", "derived-from"},
		{"T2.5", "T2.6", "supersedes"},
		{"T2.6", "T2.7", "same-as"},
	}
	for i, e := range edges {
		deps := "нет"
		status := "in_progress"
		agent := "holder-" + e.from
		if i == 0 {
			status = "todo"
		}
		deps = e.kind + ":" + e.to
		writeWhyCard(t, dir, e.from, status, agent, deps, "цепочка")
	}
	writeWhyCard(t, dir, "T2.7", "in_progress", "holder-T2.7", "нет", "цепочка")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T2.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	for _, kind := range whyEdgeKinds {
		if !strings.Contains(out, "через "+kind+":") {
			t.Errorf("в выводе нет ребра %q: %q", kind, out)
		}
	}
	for _, e := range edges {
		if !strings.Contains(out, "holder-"+e.to) {
			t.Errorf("в выводе нет держателя %q: %q", e.to, out)
		}
	}
}

func TestTaskCommandWhyIgnoresBlockedText(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T3.1", "todo", "", "нет", "карточка blocked без цепочки")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T3.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "цепочка блокировок не найдена") {
		t.Errorf("нет сообщения о пустой цепочке: %q", out)
	}
	if strings.Contains(out, "blocked") {
		t.Errorf("вывод построен по тексту статуса: %q", out)
	}
}

func TestTaskCommandWhyNewIDForms(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "ATX-R01", "in_progress", "holder-atx", "нет", "цепочка")
	writeWhyCard(t, dir, "RS-CMP-06", "in_progress", "holder-rs", "нет", "цепочка")
	writeWhyCard(t, dir, "T9.1", "todo", "", "depends-on:ATX-R01", "цепочка")
	writeWhyCard(t, dir, "T9.2", "todo", "", "depends-on:RS-CMP-06", "цепочка")

	for _, tc := range []struct{ start, target, holder string }{
		{"T9.1", "ATX-R01", "holder-atx"},
		{"T9.2", "RS-CMP-06", "holder-rs"},
	} {
		var stdout, stderr bytes.Buffer
		code := TaskCommandWithConfig([]string{"task", "why", tc.start}, &stdout, &stderr, whyConfig(dir))
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("why %s: код %d, stderr %q", tc.start, code, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{tc.target, tc.holder, "через depends-on"} {
			if !strings.Contains(out, want) {
				t.Errorf("why %s: в выводе нет %q: %q", tc.start, want, out)
			}
		}
	}
}

func TestTaskCommandWhyTreatsDuplicateAsClosed(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T5.1", "todo", "", "T5.2", "цепочка")
	writeWhyCard(t, dir, "T5.2", "duplicate", "holder-dup", "T5.3", "цепочка")
	writeWhyCard(t, dir, "T5.3", "todo", "", "нет", "цепочка")

	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "why", "T5.1"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "T5.2") {
		t.Errorf("дубликат попал в цепочку: %q", out)
	}
	if !strings.Contains(out, "цепочка блокировок не найдена") {
		t.Errorf("нет сообщения о пустой цепочке: %q", out)
	}
}

func TestTaskCommandWhyValidation(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T4.1", "todo", "", "нет", "цепочка")
	cfg := whyConfig(dir)
	cases := []struct {
		name string
		argv []string
	}{
		{"без ID", []string{"task", "why"}},
		{"два ID", []string{"task", "why", "T4.1", "T4.2"}},
		{"неверный ID", []string{"task", "why", "four.one"}},
		{"неизвестная карточка", []string{"task", "why", "T4.9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, cfg)
			if code != 1 {
				t.Errorf("код = %d, ждали 1", code)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

// ---- CBX-02: один предикат GATED для всех поверхностей ----

func cbxCard(id, status string, extra map[string]string) taskstore.Card {
	if extra == nil {
		extra = map[string]string{}
	}
	return taskstore.Card{ID: id, Status: status, Extra: extra, Context: taskstore.GateDescription(id, "нужен обзор")}
}

func cbxIDs(refs []taskstore.GateRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

func TestGatesHoldingPredicate(t *testing.T) {
	subject := cbxCard("T1.1", "todo", nil)
	gate := cbxCard("T1.2", "todo", map[string]string{"await-type": "human"})

	refs := taskstore.GatesHolding(subject, []taskstore.GateDep{
		{Kind: "depends-on", Target: gate},
	})
	if len(refs) != 1 || refs[0].ID != "T1.2" {
		t.Fatalf("открытый гейт: %+v", refs)
	}
	if refs[0].Type != "human" || refs[0].Reason != "нужен обзор" {
		t.Fatalf("type/reason = %q/%q", refs[0].Type, refs[0].Reason)
	}

	// Закрытый гейт не удерживает.
	closed := gate
	closed.Status = "done"
	if got := taskstore.GatesHolding(subject, []taskstore.GateDep{{Kind: "depends-on", Target: closed}}); len(got) != 0 {
		t.Fatalf("закрытый гейт дал %+v", got)
	}

	// Закрытая и закреплённая карточка-субъект не помечаются.
	for _, status := range []string{"done", "dropped", "duplicate"} {
		s := subject
		s.Status = status
		if got := taskstore.GatesHolding(s, []taskstore.GateDep{{Kind: "depends-on", Target: gate}}); len(got) != 0 {
			t.Fatalf("субъект %s дал %+v", status, got)
		}
	}
	pinned := subject
	pinned.Extra = map[string]string{"pinned": "true"}
	if got := taskstore.GatesHolding(pinned, []taskstore.GateDep{{Kind: "depends-on", Target: gate}}); len(got) != 0 {
		t.Fatalf("закреплённый субъект дал %+v", got)
	}

	// waits-for-external и related не являются гейтами здесь: AN-03 и CB-04
	// владеют этими поверхностями отдельно.
	for _, kind := range []string{"waits-for-external", "related", "supersedes"} {
		if got := taskstore.GatesHolding(subject, []taskstore.GateDep{{Kind: kind, Target: gate}}); len(got) != 0 {
			t.Fatalf("ребро %s дало %+v", kind, got)
		}
	}

	// Один ID попадает в набор один раз.
	dup := taskstore.GatesHolding(subject, []taskstore.GateDep{
		{Kind: "depends-on", Target: gate},
		{Kind: "blocks", Target: gate},
	})
	if len(dup) != 1 {
		t.Fatalf("дубль гейта: %+v", dup)
	}
}

func TestGateRefJSONHasExactlyThreeKeys(t *testing.T) {
	b, err := json.Marshal(taskstore.GateRef{ID: "T1.2", Type: "human", Reason: "нужен обзор"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("ключей %d, ждали 3: %s", len(m), b)
	}
	for _, key := range []string{"id", "type", "reason"} {
		if _, ok := m[key]; !ok {
			t.Errorf("нет ключа %q: %s", key, b)
		}
	}
}

func TestGateReasonRoundTrip(t *testing.T) {
	desc := taskstore.GateDescription("T1.1", "  нужен обзор  ")
	if got := taskstore.GateReason(desc); got != "нужен обзор" {
		t.Fatalf("GateReason = %q", got)
	}
	if got := taskstore.GateReason("описание без причины"); got != "" {
		t.Fatalf("GateReason без маркера = %q", got)
	}
}

func cbxByID(t *testing.T, dir string) map[string]taskstore.Card {
	t.Helper()
	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]taskstore.Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	return byID
}

func cbxSurfaceIDs(t *testing.T, dir, id string) (predicate, whyText, whyJSON, listJSON []string) {
	t.Helper()
	cfg := whyConfig(dir)
	byID := cbxByID(t, dir)
	predicate = cbxIDs(gatedByFor(id, byID))

	var out, errOut bytes.Buffer
	if code := TaskCommandWithConfig([]string{"task", "why", id}, &out, &errOut, cfg); code != 0 || errOut.Len() != 0 {
		t.Fatalf("why: код %d stderr %q", code, errOut.String())
	}
	text := out.String()
	var line string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "gated_by:") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("в why нет строки gated_by: %q", text)
	}
	if len(predicate) == 0 {
		if !strings.Contains(line, "пусто") {
			t.Errorf("gated_by-строка = %q, ждали пусто", line)
		}
	} else {
		for _, refID := range predicate {
			if !strings.Contains(line, refID) {
				t.Errorf("в why-строке нет ID %s: %q", refID, line)
			}
		}
		for _, part := range strings.Split(strings.TrimSpace(strings.SplitN(line, "gated_by:", 2)[1]), ", ") {
			id := strings.SplitN(strings.TrimSpace(part), " (", 2)[0]
			if id != "" && id != "пусто" {
				whyText = append(whyText, id)
			}
		}
	}
	sort.Strings(whyText)

	out.Reset()
	errOut.Reset()
	if code := TaskCommandWithConfig([]string{"task", "why", id, "--format", "json"}, &out, &errOut, cfg); code != 0 || errOut.Len() != 0 {
		t.Fatalf("why json: код %d stderr %q", code, errOut.String())
	}
	var w struct {
		GatedBy []taskstore.GateRef `json:"gated_by"`
	}
	if err := json.Unmarshal(out.Bytes(), &w); err != nil {
		t.Fatalf("why json: %v", err)
	}
	whyJSON = cbxIDs(w.GatedBy)

	out.Reset()
	errOut.Reset()
	if code := TaskCommandWithConfig([]string{"task", "list", "--format", "json"}, &out, &errOut, cfg); code != 0 || errOut.Len() != 0 {
		t.Fatalf("list json: код %d stderr %q", code, errOut.String())
	}
	var rows []struct {
		ID      string              `json:"id"`
		GatedBy []taskstore.GateRef `json:"gated_by"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("list json: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			listJSON = cbxIDs(row.GatedBy)
		}
	}
	return predicate, whyText, whyJSON, listJSON
}

func cbxSameSet(t *testing.T, predicate, a, b, c []string) {
	t.Helper()
	want := strings.Join(predicate, ",")
	for name, got := range map[string][]string{"why": a, "whyJSON": b, "listJSON": c} {
		if strings.Join(got, ",") != want {
			t.Errorf("%s = %v, предикат = %v", name, got, predicate)
		}
	}
}

func TestGatedBySurfacesAgreeAndClearTogether(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T1.1", "todo", "", "depends-on:T1.2", "цепочка")
	writeWhyCard(t, dir, "T1.2", "todo", "alice", "нет", "гейт")

	p, a, b, c := cbxSurfaceIDs(t, dir, "T1.1")
	if len(p) != 1 || p[0] != "T1.2" {
		t.Fatalf("открытый гейт: предикат = %v", p)
	}
	cbxSameSet(t, p, a, b, c)

	// Закрытие гейта одновременно очищает все поверхности.
	writeWhyCard(t, dir, "T1.2", "done", "alice", "нет", "гейт")
	p, a, b, c = cbxSurfaceIDs(t, dir, "T1.1")
	if len(p) != 0 {
		t.Fatalf("закрытый гейт: предикат = %v", p)
	}
	cbxSameSet(t, p, a, b, c)
}

func TestGatedByClosedSubjectAndWaitsForEmpty(t *testing.T) {
	dir := t.TempDir()
	writeWhyCard(t, dir, "T1.1", "done", "", "depends-on:T1.2", "закрытый субъект")
	writeWhyCard(t, dir, "T1.2", "todo", "alice", "нет", "гейт")
	p, a, b, c := cbxSurfaceIDs(t, dir, "T1.1")
	if len(p) != 0 {
		t.Fatalf("закрытый субъект: %v", p)
	}
	cbxSameSet(t, p, a, b, c)

	dir = t.TempDir()
	writeWhyCard(t, dir, "T2.1", "todo", "", "waits-for-external gh:pr:42", "внешний гейт")
	p, a, b, c = cbxSurfaceIDs(t, dir, "T2.1")
	if len(p) != 0 {
		t.Fatalf("waits-for-external: %v", p)
	}
	cbxSameSet(t, p, a, b, c)
}

func TestGatedByTwentyGraphsNoDivergence(t *testing.T) {
	divergences := 0
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		gateStatus := "todo"
		if i%3 == 0 {
			gateStatus = "done"
		}
		subjectStatus := "todo"
		if i%5 == 0 {
			subjectStatus = "done"
		}
		deps := "depends-on:T1.2"
		if i%4 == 0 {
			deps = "waits-for-external gh:pr:42"
		}
		writeWhyCard(t, dir, "T1.1", subjectStatus, "", deps, "граф")
		writeWhyCard(t, dir, "T1.2", gateStatus, "alice", "нет", "гейт")

		p, a, b, c := cbxSurfaceIDs(t, dir, "T1.1")
		want := ""
		if subjectStatus != "done" && gateStatus != "done" && i%4 != 0 {
			want = "T1.2"
		}
		if strings.Join(p, ",") != want {
			t.Errorf("граф %d: предикат %v, ждали %q", i, p, want)
		}
		if strings.Join(a, ",") != want || strings.Join(b, ",") != want || strings.Join(c, ",") != want {
			divergences++
			t.Errorf("граф %d: why=%v whyJSON=%v list=%v, ждали %q", i, a, b, c, want)
		}
	}
	if divergences != 0 {
		t.Fatalf("расхождений между поверхностями: %d", divergences)
	}
}

func TestFindProjectRoot(t *testing.T) {
	root := findProjectRoot()
	if root == "" {
		t.Fatal("пустой корень проекта")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("go.mod не найден в %s: %v", root, err)
	}
}

// ---- ADR 0051 п4 / T3.120 ----

func writeCompletionCard(t *testing.T, dir, id, status string) string {
	t.Helper()
	body := fmt.Sprintf(`# %s. Карточка завершения
Тип: task
Статус: %s
Фаза: P1
Владелец файлов: cmd/beacon/task.go
Зависимости: нет
Ссылка: ADR 0001
Агент: тест
Время: 2026-11-20T00:00:00Z
Цель: Проверить завершение
Контекст: ADR 0051 п4
Шаги: Шаг
Проверка: Проверка
Приёмка: Приёмка
`, id, status)
	path := filepath.Join(dir, id+".md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskCommandDoneRejectsMissingEvidence(t *testing.T) {
	cases := []struct {
		name   string
		argv   []string
		want   string
		parked bool
	}{
		{"без полей", []string{"task", "done", "T5.1"}, "доказательства", true},
		{"только итог", []string{"task", "done", "T5.1", "--summary", "готово"}, "доказательства", true},
		{"пустое доказательство", []string{"task", "done", "T5.1", "--summary", "готово", "--evidence", "   "}, "доказательства", true},
		{"только доказательство", []string{"task", "done", "T5.1", "--evidence", "go test"}, "итога", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeCompletionCard(t, dir, "T5.1", "in_progress")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, whyConfig(dir))
			if code != 1 {
				t.Errorf("код = %d, ждали 1", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout не пустой при отказе: %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("в stderr нет %q: %q", tc.want, stderr.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.parked {
				if string(after) != string(before) {
					t.Errorf("карточка изменена при отказе: %q", after)
				}
				return
			}
			// Отказ без доказательства не закрывает задачу и оставляет её в needs_input.
			body := string(after)
			if strings.Contains(body, "Статус: done") {
				t.Errorf("задача закрыта без доказательства: %q", body)
			}
			for _, want := range []string{"Статус: in_progress", "Состояние: needs_input", "--evidence"} {
				if !strings.Contains(body, want) {
					t.Errorf("в карточке нет %q: %q", want, body)
				}
			}
			cards, err := taskstore.LoadCards(dir)
			if err != nil {
				t.Fatalf("карточка в needs_input не разбирается: %v", err)
			}
			if len(cards) != 1 || cards[0].Status != "in_progress" {
				t.Fatalf("карточки после отказа: %+v", cards)
			}
		})
	}
}

func TestTaskCommandDoneWithEvidence(t *testing.T) {
	dir := t.TempDir()
	path := writeCompletionCard(t, dir, "T5.2", "in_progress")
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "done", "T5.2",
		"--summary", "фича готова", "--evidence", "go test ./cmd/beacon\n"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "T5.2 done" {
		t.Fatalf("stdout %q", stdout.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Статус: done", "Итог: фича готова", "Доказательство: go test ./cmd/beacon"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в карточке нет %q: %q", want, body)
		}
	}
	// Карточка с новыми полями обязана разбираться основным парсером.
	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		t.Fatalf("карточка после done не разбирается: %v", err)
	}
	if len(cards) != 1 || cards[0].Status != "done" {
		t.Fatalf("карточки после done: %+v", cards)
	}
	// Повторное завершение готовой карточки отклоняется.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "done", "T5.2",
		"--summary", "ещё раз", "--evidence", "go test"}, &stdout, &stderr, whyConfig(dir))
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("повторный done: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestTaskCommandNeedsInput(t *testing.T) {
	dir := t.TempDir()
	path := writeCompletionCard(t, dir, "T5.3", "todo")
	var stdout, stderr bytes.Buffer

	// Завершение без доказательства не закрывает карточку и ставит needs_input.
	parkPath := writeCompletionCard(t, dir, "T5.5", "in_progress")
	stdout.Reset()
	stderr.Reset()
	code := TaskCommandWithConfig([]string{"task", "done", "T5.5", "--summary", "готово"},
		&stdout, &stderr, whyConfig(dir))
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("done без доказательства: код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	parked, err := os.ReadFile(parkPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(parked), "Статус: done") ||
		!strings.Contains(string(parked), "Состояние: needs_input") {
		t.Fatalf("карточка без доказательства не переведена в needs_input: %q", parked)
	}
	stdout.Reset()
	stderr.Reset()

	code = TaskCommandWithConfig([]string{"task", "needs-input", "T5.3",
		"--question", "нужен доступ к PR?"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("needs-input: код %d stderr %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "T5.3 needs_input" {
		t.Fatalf("stdout %q", stdout.String())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Статус: in_progress", "Состояние: needs_input", "Вопрос: нужен доступ к PR?"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("в карточке нет %q: %q", want, body)
		}
	}
	cards, err := taskstore.LoadCards(dir)
	if err != nil {
		t.Fatalf("карточка с needs_input не разбирается: %v", err)
	}
	found := false
	for _, c := range cards {
		if c.ID == "T5.3" {
			found = true
			if c.Status != "in_progress" {
				t.Fatalf("статус T5.3 = %q, ждали in_progress", c.Status)
			}
		}
	}
	if !found {
		t.Fatalf("карточка T5.3 не найдена: %+v", cards)
	}

	// Завершение после ответа снимает маркер needs_input и требует доказательство.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"task", "done", "T5.3",
		"--summary", "ответ получен", "--evidence", "gh issue view 1 --comments"}, &stdout, &stderr, whyConfig(dir))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("done после needs_input: код %d stderr %q", code, stderr.String())
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Статус: done") {
		t.Errorf("нет статуса done: %q", body)
	}
	if strings.Contains(string(body), "needs_input") || strings.Contains(string(body), "Вопрос:") {
		t.Errorf("маркер needs_input не снят: %q", body)
	}
}

func TestTaskCommandNeedsInputRequiresQuestion(t *testing.T) {
	dir := t.TempDir()
	path := writeCompletionCard(t, dir, "T5.4", "in_progress")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "needs-input", "T5.4"}, &stdout, &stderr, whyConfig(dir))
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--question") {
		t.Fatalf("код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("карточка изменена при отказе: %q", after)
	}
}

// ---- ADR 0056 п5 / T3.126: wait --after SEQ ----

// seedWaitEvents пишет в журнал три события с телами alpha, beta, gamma.
func seedWaitEvents(t *testing.T, stateDir string) {
	t.Helper()
	db, err := store.Open(filepath.Join(stateDir, mcpStateDBName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rw, err := store.NewRedactingWriter(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"alpha", "beta", "gamma"} {
		payload := fmt.Sprintf(`{"kind":"progress","message":%q}`, msg)
		if err := rw.WriteEvent(context.Background(), "watch-A", []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTaskCommandWaitAfterSeq(t *testing.T) {
	dir := t.TempDir()
	seedWaitEvents(t, dir)
	cfg := taskcli.Config{StateDir: dir, Root: dir}
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "wait", "--after", "1"}, &stdout, &stderr, cfg)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "alpha") {
		t.Errorf("событие до SEQ возвращено: %q", out)
	}
	for _, want := range []string{"beta", "gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет события %q: %q", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("строк = %d, ждали 2: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "2 ") || !strings.HasPrefix(lines[1], "3 ") {
		t.Errorf("неверный порядок или позиции: %q", out)
	}
}

func TestTaskCommandWaitResumesFromLastSeq(t *testing.T) {
	dir := t.TempDir()
	seedWaitEvents(t, dir)
	cfg := taskcli.Config{StateDir: dir, Root: dir}
	var stdout, stderr bytes.Buffer

	// Повтор с последней позицией ничего не возвращает: опрос не нужен.
	code := TaskCommandWithConfig([]string{"task", "wait", "--after", "3"}, &stdout, &stderr, cfg)
	if code != 0 || stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}

	// Форма без префикса task и --after 0 возвращают все события.
	stdout.Reset()
	stderr.Reset()
	code = TaskCommandWithConfig([]string{"wait", "--after", "0"}, &stdout, &stderr, cfg)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("код %d stderr %q", code, stderr.String())
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("в выводе нет %q: %q", want, stdout.String())
		}
	}
}

func TestTaskCommandWaitEmptyJournal(t *testing.T) {
	dir := t.TempDir()
	cfg := taskcli.Config{StateDir: dir, Root: dir}
	var stdout, stderr bytes.Buffer
	code := TaskCommandWithConfig([]string{"task", "wait", "--after", "0"}, &stdout, &stderr, cfg)
	if code != 0 || stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("код %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestTaskCommandWaitValidation(t *testing.T) {
	dir := t.TempDir()
	seedWaitEvents(t, dir)
	cfg := taskcli.Config{StateDir: dir, Root: dir}
	cases := []struct {
		name string
		argv []string
	}{
		{"без --after", []string{"task", "wait"}},
		{"не число", []string{"task", "wait", "--after", "seq"}},
		{"отрицательное", []string{"task", "wait", "--after", "-1"}},
		{"лишний аргумент", []string{"task", "wait", "--after", "1", "T1.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := TaskCommandWithConfig(tc.argv, &stdout, &stderr, cfg)
			if code != 1 {
				t.Errorf("код = %d, ждали 1", code)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func writeLaunchCardForTest(t *testing.T, dir, id, status, owners string) {
	t.Helper()
	text := fmt.Sprintf("# %s. Тест\nСтатус: %s\nФаза: P2\nТип: task\n"+
		"Владелец файлов: %s\nЗависимости: нет\nСсылка: ADR 0001\nАгент: нет\nВремя: нет\n"+
		"Цель: ц\nКонтекст: к\nШаги: ш\nПроверка: п\nПриёмка: пр\n",
		id, status, owners)
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestTaskCheckLaunchCommand(t *testing.T) {
	root := t.TempDir()
	tasks := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(tasks, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(root)
	writeLaunchCardForTest(t, tasks, "T7.1", "todo", "internal/app/a.go")
	writeLaunchCardForTest(t, tasks, "T7.2", "todo", "internal/infra/*.go")
	writeLaunchCardForTest(t, tasks, "T7.3", "todo", "internal/app/*.go")

	var stdout, stderr bytes.Buffer
	if code := TaskCommandWithConfig([]string{"task", "check-launch", "T7.1", "T7.2"}, &stdout, &stderr, cfg); code != 0 || stderr.Len() != 0 {
		t.Fatalf("разрешённый план: код %d stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "разрешён") {
		t.Errorf("нет подтверждения запуска: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := TaskCommandWithConfig([]string{"task", "check-launch", "T7.1", "T7.3"}, &stdout, &stderr, cfg); code != 1 {
		t.Fatalf("пересечение по glob: код %d stderr %q", code, stderr.String())
	}
	for _, want := range []string{"T7.1", "T7.3", "отменён"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("в отказе нет %q: %q", want, stderr.String())
		}
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout не пустой при отказе: %q", stdout.String())
	}

	for _, argv := range [][]string{
		{"task", "check-launch"},
		{"task", "check-launch", "T9.9"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := TaskCommandWithConfig(argv, &stdout, &stderr, cfg); code != 1 {
			t.Errorf("%v: код %d, ждали 1", argv, code)
		}
	}
}

func TestTaskLeaseAcquireReleaseAndCheck(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer

	acquire := []string{"task", "lease", "acquire", "--agent", "agent-a",
		"--glob", "internal/app/*.go", "--worktree", "worktrees/w7-c"}
	if code := TaskCommandWithConfig(acquire, &stdout, &stderr, cfg); code != 0 {
		t.Fatalf("acquire: код %d stderr %q", code, stderr.String())
	}

	run := func(argv []string) (int, string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		code := TaskCommandWithConfig(argv, &stdout, &stderr, cfg)
		return code, stdout.String() + stderr.String()
	}

	code, out := run([]string{"task", "lease", "check", "--agent", "agent-b", "--file", "internal/app/service.go"})
	if code != leaseExitBlocked || !strings.Contains(out, "agent-a") {
		t.Fatalf("чужой файл: код %d вывод %q", code, out)
	}
	code, out = run([]string{"task", "lease", "check", "--agent", "agent-b", "--worktree", "worktrees/w7-c/task.go"})
	if code != leaseExitBlocked || !strings.Contains(out, "agent-a") {
		t.Fatalf("чужой ворктри: код %d вывод %q", code, out)
	}
	code, out = run([]string{"task", "lease", "check", "--agent", "agent-a", "--file", "internal/app/service.go"})
	if code != 0 {
		t.Fatalf("своя аренда заблокировала: код %d вывод %q", code, out)
	}

	if code, out = run([]string{"task", "lease", "release", "--agent", "agent-a"}); code != 0 {
		t.Fatalf("release: код %d вывод %q", code, out)
	}
	code, out = run([]string{"task", "lease", "check", "--agent", "agent-b", "--file", "internal/app/service.go"})
	if code != 0 {
		t.Fatalf("после снятия аренда блокирует: код %d вывод %q", code, out)
	}
}

func TestTaskLeaseValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := taskcli.Config{StateDir: dir, Root: dir}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"task", "lease"},
		{"task", "lease", "acquire", "--glob", "a.go"},
		{"task", "lease", "acquire", "--agent", "agent-a"},
		{"task", "lease", "check", "--agent", "agent-b"},
		{"task", "lease", "check", "--agent", "agent-b", "--file", "a.go", "--bogus"},
		{"task", "lease", "frobnicate"},
	} {
		var stdout, stderr bytes.Buffer
		code := TaskCommandWithConfig(argv, &stdout, &stderr, cfg)
		if code != 1 {
			t.Errorf("%v: код %d, ждали 1", argv, code)
		}
	}
}

// ---- BS.32: CLI управления расписаниями через порт ScheduleStore ----

type failingScheduleStore struct{}

func (failingScheduleStore) Create(context.Context, server.Schedule) error {
	return errors.New("store down")
}
func (failingScheduleStore) Get(context.Context, string) (server.Schedule, error) {
	return server.Schedule{}, errors.New("store down")
}
func (failingScheduleStore) List(context.Context) ([]server.Schedule, error) {
	return nil, errors.New("store down")
}
func (failingScheduleStore) Update(context.Context, server.Schedule) error {
	return errors.New("store down")
}
func (failingScheduleStore) Delete(context.Context, string) error {
	return errors.New("store down")
}

func withScheduleStore(t *testing.T, store server.ScheduleStore, fn func(taskcli.Config)) {
	t.Helper()
	orig := scheduleStoreFor
	defer func() { scheduleStoreFor = orig }()
	scheduleStoreFor = func(taskcli.Config) (server.ScheduleStore, error) { return store, nil }
	dir := t.TempDir()
	fn(taskcli.Config{StateDir: dir, Root: dir})
}

func TestTaskScheduleCreateListPauseDelete(t *testing.T) {
	withScheduleStore(t, server.NewMemoryScheduleStore(), func(cfg taskcli.Config) {
		run := func(argv ...string) (int, string, string) {
			var out, errOut bytes.Buffer
			code := TaskCommandWithConfig(argv, &out, &errOut, cfg)
			return code, out.String(), errOut.String()
		}

		code, out, errOut := run("task", "schedule", "create", "--id", "s1",
			"--task-id", "BS.32", "--kind", "interval", "--expr", "15m", "--timezone", "UTC")
		if code != 0 || !strings.Contains(out, "s1 scheduled") {
			t.Fatalf("create: код %d stdout %q stderr %q", code, out, errOut)
		}

		code, out, errOut = run("task", "schedule", "list")
		if code != 0 || !strings.Contains(out, "s1") || !strings.Contains(out, "BS.32") || !strings.Contains(out, "true") {
			t.Fatalf("list: код %d stdout %q stderr %q", code, out, errOut)
		}

		// Пауза сохраняет задачу и выключает её, не удаляя.
		code, out, errOut = run("task", "schedule", "--id", "s1", "--enabled=false")
		if code != 0 || !strings.Contains(out, "s1 paused") {
			t.Fatalf("pause: код %d stdout %q stderr %q", code, out, errOut)
		}
		code, out, errOut = run("task", "schedule", "list", "--format", "json")
		if code != 0 {
			t.Fatalf("list json: код %d stderr %q", code, errOut)
		}
		var rows []server.Schedule
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("неверный JSON: %v, вывод %q", err, out)
		}
		if len(rows) != 1 || rows[0].ID != "s1" || rows[0].Enabled || rows[0].TaskID != "BS.32" {
			t.Fatalf("после паузы: %+v", rows)
		}

		code, out, errOut = run("task", "schedule", "resume", "--id", "s1", "--enabled=true")
		if code != 0 || !strings.Contains(out, "s1 resumed") {
			t.Fatalf("resume: код %d stdout %q stderr %q", code, out, errOut)
		}

		code, out, errOut = run("task", "schedule", "delete", "--id", "s1")
		if code != 0 || !strings.Contains(out, "s1 deleted") {
			t.Fatalf("delete: код %d stdout %q stderr %q", code, out, errOut)
		}
		code, out, errOut = run("task", "schedule", "list")
		if code != 0 || strings.Contains(out, "s1") {
			t.Fatalf("задача пережила удаление: код %d stdout %q stderr %q", code, out, errOut)
		}
	})
}

func TestTaskScheduleStoreErrorAndValidation(t *testing.T) {
	withScheduleStore(t, failingScheduleStore{}, func(cfg taskcli.Config) {
		var out, errOut bytes.Buffer
		code := TaskCommandWithConfig([]string{"task", "schedule", "list"}, &out, &errOut, cfg)
		if code != taskstore.ExitStartup || !strings.Contains(errOut.String(), "store down") {
			t.Fatalf("ошибка стора: код %d stderr %q", code, errOut.String())
		}
	})

	withScheduleStore(t, server.NewMemoryScheduleStore(), func(cfg taskcli.Config) {
		for _, argv := range [][]string{
			{"task", "schedule"},
			{"task", "schedule", "--task-id", "T"},
			{"task", "schedule", "--task-id", "T", "--kind", "bogus", "--expr", "x"},
			{"task", "schedule", "list", "--format", "yaml"},
			{"task", "schedule", "delete"},
		} {
			var out, errOut bytes.Buffer
			code := TaskCommandWithConfig(argv, &out, &errOut, cfg)
			if code != taskstore.ExitValidation || out.Len() != 0 {
				t.Errorf("%v: код %d stdout %q stderr %q", argv, code, out.String(), errOut.String())
			}
		}
	})
}

// Порт реализован JSON-файлом, поэтому расписания переживают новый процесс CLI.
func TestFileScheduleStorePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "schedules.json")
	storeA, err := server.NewFileScheduleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svcA, err := server.NewScheduleService(storeA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.Schedule(context.Background(), server.ScheduleCreate{
		ID: "s1", TaskID: "BS.32", Kind: server.KindMark, Expr: "deploy",
	}); err != nil {
		t.Fatal(err)
	}
	storeB, err := server.NewFileScheduleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := storeB.Get(context.Background(), "s1")
	if err != nil || got.ID != "s1" || got.TaskID != "BS.32" || !got.Enabled {
		t.Fatalf("после перезапуска: %+v, %v", got, err)
	}
}
