package taskstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validCard = `# T2.4. Пример карточки
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/server.go, internal/infra/mcp/server_test.go
Зависимости: T1.1
Ссылка: ADR 0003
Агент: нет
Время: нет
Цель: цель
Контекст: контекст
Шаги: шаги
Проверка: проверка
Приёмка: приёмка
Гейт: G2
`

func TestParseCardValid(t *testing.T) {
	c, err := ParseCard("T2.4.md", []byte(validCard))
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	checks := map[string]bool{
		"id":       c.ID == "T2.4",
		"type":     c.Type == "task",
		"status":   c.Status == "todo",
		"phase":    c.Phase == "P2",
		"title":    c.Title == "Пример карточки",
		"owners":   len(c.OwnerFiles) == 2 && c.OwnerFiles[0] == "internal/infra/mcp/server.go",
		"deps":     len(c.Dependencies) == 1 && c.Dependencies[0] == "T1.1",
		"ref":      c.Reference == "ADR 0003",
		"extra":    c.Extra["Гейт"] == "G2",
		"groupNum": c.Group == 2 && c.Number == 4,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("поле %s разобрано неверно: %+v", name, c)
		}
	}
}

func TestParseCardFromSuffixedName(t *testing.T) {
	c, err := ParseCard("T3.7-executor-task-template.md", []byte(validCard))
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	if c.ID != "T3.7" {
		t.Fatalf("ID = %q, ждали T3.7", c.ID)
	}
}

func TestParseCardViolations(t *testing.T) {
	cases := []struct {
		name, fileName, text, want string
	}{
		{"нетСтатуса", "T2.4.md", strings.Replace(validCard, "Статус: todo\n", "", 1), "Статус"},
		{"повторТипа", "T2.4.md", strings.Replace(validCard, "Тип: task\n", "Тип: task\nТип: epic\n", 1), "Тип"},
		{"неверныйСтатус", "T2.4.md", strings.Replace(validCard, "Статус: todo", "Статус: open", 1), "Статус"},
		{"невернаяФаза", "T2.4.md", strings.Replace(validCard, "Фаза: P2", "Фаза: P9", 1), "Фаза"},
		{"нетIDвИмени", "readme.md", validCard, "имя файла"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCard(tc.fileName, []byte(tc.text))
			if err == nil {
				t.Fatalf("ParseCard не нашёл нарушение")
			}
			if Code(err) != ExitValidation {
				t.Errorf("код = %d, ждали 1", Code(err))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ошибка %q не содержит %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadCardsSortAndViolation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("T2.4.md", validCard)
	write("T1.1.md", strings.Replace(validCard, "T2.4", "T1.1", 1))
	write("notes.txt", "не карточка")
	cards, err := LoadCards(dir)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	if len(cards) != 2 || cards[0].ID != "T1.1" || cards[1].ID != "T2.4" {
		t.Fatalf("порядок карточек неверный: %+v", cards)
	}
	write("T0.1.md", strings.Replace(validCard, "Статус: todo\n", "", 1))
	_, err = LoadCards(dir)
	if err == nil || !strings.Contains(err.Error(), "T0.1.md") || !strings.Contains(err.Error(), "Статус") {
		t.Fatalf("LoadCards должен сообщить об имени файла и поле, получил %v", err)
	}
}

func TestNormalizePathAndConflict(t *testing.T) {
	cases := []struct{ in, want string }{
		{`internal\infra\mcp\`, "internal/infra/mcp"},
		{"cmd/beacon/**", "cmd/beacon"},
		{"cmd/beacon/", "cmd/beacon"},
		{"./internal/./infra", "internal/infra"},
	}
	for _, tc := range cases {
		if got := NormalizePath(tc.in); got != tc.want {
			t.Errorf("NormalizePath(%q) = %q, ждали %q", tc.in, got, tc.want)
		}
	}
	pairs := []struct {
		a, b string
		want bool
	}{
		{"cmd/beacon/**", "cmd/beacon/main.go", true},
		{"internal/infra/github/testdata/", "internal/infra/github/testdata/x.json", true},
		{"internal/infra/mcp/server.go", "internal/infra/mcp/server.go", true},
		{"internal/infra/mcp", "internal/infra/mcp2/server.go", false},
		{"", "internal/app/a.go", false},
	}
	for _, p := range pairs {
		if got := PathsConflict(p.a, p.b); got != p.want {
			t.Errorf("PathsConflict(%q,%q) = %v, ждали %v", p.a, p.b, got, p.want)
		}
	}
}

func TestLockDeadPIDAndRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "task-test.lock")
	dead := fmt.Sprintf("pid=99999999\nstarted=%s\nroot=%s\n", time.Now().UTC().Format(time.RFC3339), dir)
	if err := os.WriteFile(lockPath, []byte(dead), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(lockPath, dir)
	if err != nil {
		t.Fatalf("замок с мёртвым PID не перехвачен: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("замок не удалён: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("повторный Release должен быть безопасным: %v", err)
	}
}

func TestLockStaleWithLivePID(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "task-test.lock")
	old := fmt.Sprintf("pid=%d\nstarted=%s\nroot=%s\n", os.Getpid(),
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), dir)
	if err := os.WriteFile(lockPath, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(lockPath, dir)
	if err != nil {
		t.Fatalf("устаревший замок не перехвачен: %v", err)
	}
	_ = lock.Release()
}

func TestCounterAndNextID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "counter.json")
	groups, err := ReadCounter(p)
	if err != nil || len(groups) != 0 {
		t.Fatalf("отсутствующий счётчик: %v, %v", groups, err)
	}
	groups["2"] = 4
	if err := WriteCounter(p, groups); err != nil {
		t.Fatal(err)
	}
	groups, err = ReadCounter(p)
	if err != nil || groups["2"] != 4 {
		t.Fatalf("счётчик не сохранён: %v, %v", groups, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".beacon-tmp-") {
			t.Fatalf("остался временный файл %s", e.Name())
		}
	}
	cards := []Card{{Group: 2, Number: 3}, {Group: 2, Number: 2}, {Group: 1, Number: 9}}
	n, id := NextID(cards, groups, 2)
	if n != 5 || id != "T2.5" {
		t.Fatalf("NextID = %d, %q, ждали 5, T2.5", n, id)
	}
	n, id = NextID(cards, map[string]int{}, 3)
	if n != 1 || id != "T3.1" {
		t.Fatalf("NextID для новой группы = %d, %q, ждали 1, T3.1", n, id)
	}
}

func TestCreateExclusiveAndAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "T2.1.md")
	if err := CreateExclusive(p, []byte("первый\n")); err != nil {
		t.Fatal(err)
	}
	if err := CreateExclusive(p, []byte("второй\n")); Code(err) != ExitValidation || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("повторное создание должно давать код 1, получил %v", err)
	}
	if err := AtomicWrite(p, []byte("третий\n"), 0644); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "третий\n" {
		t.Fatalf("содержимое = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("остались лишние файлы: %v", entries)
	}
}

func TestReplaceFields(t *testing.T) {
	got := ReplaceFields(validCard, "agent-1", "2026-11-19T14:03:21Z")
	wantChecks := []string{
		"Статус: in_progress", "Агент: agent-1", "Время: 2026-11-19T14:03:21Z",
		"Зависимости: T1.1", "Гейт: G2",
	}
	for _, want := range wantChecks {
		if !strings.Contains(got, want) {
			t.Errorf("нет строки %q в:\n%s", want, got)
		}
	}
	again := ReplaceFields(got, "agent-2", "2026-11-19T15:00:00Z")
	if again != got {
		t.Fatal("ReplaceFields изменил карточку, статус которой уже in_progress")
	}
}

func taskEventTxFixture(t *testing.T) TaskEventTx {
	t.Helper()
	return TaskEventTx{
		Path: filepath.Join(t.TempDir(), "T2.4.md"),
		Card: []byte(validCard),
		Event: TaskEvent{
			ID:     "e1",
			Kind:   "created",
			TaskID: "T2.4",
			At:     time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
		},
	}
}

// assertPaired проверяет приёмку карточки: задача и событие есть вместе или
// нет ни того, ни другого.
func assertPaired(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	hasTask := err == nil
	if hasTask {
		_, err = ParseCard(filepath.Base(path), data)
		hasTask = err == nil
	}
	events, err := ReadTaskEvents(path)
	hasEvents := err == nil && len(events) > 0
	if hasTask != hasEvents {
		t.Fatalf("задача без события или событие без задачи: задача=%v, события=%d", hasTask, len(events))
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".beacon-tmp-") {
			t.Fatalf("остался временный файл %s", e.Name())
		}
	}
}

func interruptTx() error { return fmt.Errorf("имитация прерывания") }

func TestTaskEventTxInterruptPublishesNothing(t *testing.T) {
	tx := taskEventTxFixture(t)
	if err := tx.Write(interruptTx); err == nil {
		t.Fatal("прерванная транзакция должна давать ошибку")
	}
	if _, err := os.Stat(tx.Path); !os.IsNotExist(err) {
		t.Fatalf("карточка опубликована после прерывания: %v", err)
	}
	events, err := ReadTaskEvents(tx.Path)
	if err != nil || len(events) != 0 {
		t.Fatalf("событие опубликовано после прерывания: %v, %v", events, err)
	}
	assertPaired(t, tx.Path)
	assertNoTempFiles(t, filepath.Dir(tx.Path))
}

func TestTaskEventTxCommitKeepsTaskAndEventTogether(t *testing.T) {
	tx := taskEventTxFixture(t)
	if err := tx.Write(nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(tx.Path)
	if err != nil {
		t.Fatal(err)
	}
	card, err := ParseCard(filepath.Base(tx.Path), data)
	if err != nil {
		t.Fatalf("карточка после транзакции не разбирается: %v", err)
	}
	if card.ID != "T2.4" {
		t.Fatalf("ID карточки = %q, ждали T2.4", card.ID)
	}
	events, err := ReadTaskEvents(tx.Path)
	if err != nil {
		t.Fatalf("ReadTaskEvents: %v", err)
	}
	if len(events) != 1 || events[0].ID != tx.Event.ID || events[0].TaskID != card.ID {
		t.Fatalf("событие не связано с карточкой: %+v, карточка %q", events, card.ID)
	}
	assertPaired(t, tx.Path)
	assertNoTempFiles(t, filepath.Dir(tx.Path))
}

func TestTaskEventTxInterruptDoesNotSplitExistingRecord(t *testing.T) {
	tx := taskEventTxFixture(t)
	if err := tx.Write(nil); err != nil {
		t.Fatalf("первый Write: %v", err)
	}
	before, err := os.ReadFile(tx.Path)
	if err != nil {
		t.Fatal(err)
	}

	next := TaskEventTx{
		Path: tx.Path,
		Card: before,
		Event: TaskEvent{
			ID:     "e2",
			Kind:   "taken",
			TaskID: "T2.4",
			At:     time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC),
		},
	}
	if err := next.Write(interruptTx); err == nil {
		t.Fatal("прерванная запись второго события должна давать ошибку")
	}
	after, err := os.ReadFile(tx.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("прерывание изменило уже записанную карточку")
	}
	events, err := ReadTaskEvents(tx.Path)
	if err != nil || len(events) != 1 || events[0].ID != "e1" {
		t.Fatalf("состояние разделилось: %+v, %v", events, err)
	}
	assertPaired(t, tx.Path)
	assertNoTempFiles(t, filepath.Dir(tx.Path))
}

func TestTaskEventTxRejectsMismatchedEvent(t *testing.T) {
	tx := taskEventTxFixture(t)
	tx.Event.TaskID = "T9.9"
	if err := tx.Write(nil); Code(err) != ExitValidation {
		t.Fatalf("ожидали код 1, получил %v", err)
	}
	if _, err := os.Stat(tx.Path); !os.IsNotExist(err) {
		t.Fatalf("карточка создана при неверном событии: %v", err)
	}
}

func TestTaskEventTxRepeatIsNoOp(t *testing.T) {
	tx := taskEventTxFixture(t)
	if err := tx.Write(nil); err != nil {
		t.Fatalf("первый Write: %v", err)
	}
	if err := tx.Write(nil); err != nil {
		t.Fatalf("повторный Write: %v", err)
	}
	events, err := ReadTaskEvents(tx.Path)
	if err != nil || len(events) != 1 {
		t.Fatalf("повтор добавил событие: %+v, %v", events, err)
	}
	assertPaired(t, tx.Path)
}

func TestCheckLinkageLinked(t *testing.T) {
	got := CheckLinkage(Linkage{
		Subject: "fix(store): close merged watches",
		ADRRef:  "ADR 0295",
		CardRef: "HD-022",
	}, LinkagePorts{})
	if got != LinkageLinked {
		t.Fatalf("CheckLinkage = %q, ждали %q", got, LinkageLinked)
	}
}

func TestCheckLinkageExempted(t *testing.T) {
	got := CheckLinkage(Linkage{
		Subject:       "feat: temporary hook",
		Justification: "внешний инструмент без карточки",
	}, LinkagePorts{})
	if got != LinkageExempted {
		t.Fatalf("CheckLinkage = %q, ждали %q", got, LinkageExempted)
	}
}

func TestCheckLinkageMissing(t *testing.T) {
	got := CheckLinkage(Linkage{Subject: "fix: no linkage here"}, LinkagePorts{})
	if got != LinkageMissing {
		t.Fatalf("CheckLinkage = %q, ждали %q", got, LinkageMissing)
	}
}

func TestCheckLinkageEmpty(t *testing.T) {
	if got := CheckLinkage(Linkage{}, LinkagePorts{}); got != LinkageEmpty {
		t.Fatalf("CheckLinkage = %q, ждали %q", got, LinkageEmpty)
	}
}

func TestCheckLinkageRequiresBothRefs(t *testing.T) {
	cases := []struct {
		name string
		l    Linkage
		want LinkageVerdict
	}{
		{"только ADR", Linkage{Subject: "fix: x", ADRRef: "ADR 0295"}, LinkageMissing},
		{"только карточка", Linkage{Subject: "fix: x", CardRef: "HD-022"}, LinkageMissing},
		{"обе ссылки", Linkage{Subject: "fix: x", ADRRef: "ADR 0295", CardRef: "HD-022"}, LinkageLinked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CheckLinkage(c.l, LinkagePorts{}); got != c.want {
				t.Errorf("CheckLinkage = %q, ждали %q", got, c.want)
			}
		})
	}
}

func TestCheckLinkagePorts(t *testing.T) {
	ports := LinkagePorts{
		ValidADR:  func(ref string) bool { return ref == "ADR 0295" },
		ValidCard: func(ref string) bool { return ref == "HD-022" },
	}
	bad := CheckLinkage(Linkage{Subject: "fix: x", ADRRef: "ADR 0001", CardRef: "HD-022"}, ports)
	if bad != LinkageMissing {
		t.Errorf("недействительный ADR дал %q, ждали %q", bad, LinkageMissing)
	}
	good := CheckLinkage(Linkage{Subject: "fix: x", ADRRef: "ADR 0295", CardRef: "HD-022"}, ports)
	if good != LinkageLinked {
		t.Errorf("действительные ссылки дали %q, ждали %q", good, LinkageLinked)
	}
}

func TestParseLinkage(t *testing.T) {
	msg := "fix(store): close merged watches (HD-022)\n\nСсылка на ADR 0295.\nОбоснование: внешний инструмент без карточки"
	l := ParseLinkage(msg)
	checks := map[string]bool{
		"subject":       l.Subject == "fix(store): close merged watches (HD-022)",
		"adr":           l.ADRRef == "ADR 0295",
		"card":          l.CardRef == "HD-022",
		"justification": l.Justification == "внешний инструмент без карточки",
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("поле %s разобрано неверно: %+v", name, l)
		}
	}
}

func TestRequiresLinkage(t *testing.T) {
	cases := []struct {
		subject string
		want    bool
	}{
		{"feat(store): add x", true},
		{"fix: repair y", true},
		{"feat!: break z", true},
		{"docs: update readme", false},
		{"no type here", false},
		{"", false},
	}
	for _, c := range cases {
		if got := RequiresLinkage(c.subject); got != c.want {
			t.Errorf("RequiresLinkage(%q) = %v, ждали %v", c.subject, got, c.want)
		}
	}
}
