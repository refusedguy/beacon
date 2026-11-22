package taskstore

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/observe"
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

func TestParseCardResearchLetterID(t *testing.T) {
	for _, tc := range []struct {
		file, want string
		number     int
	}{
		{"ATX-R01.md", "ATX-R01", 1},
		{"ATX-V08.md", "ATX-V08", 8},
		{"ATX-D03.md", "ATX-D03", 3},
		// A letter after the number is a separate shape. Keep both forms working.
		{"TUI-07A.md", "TUI-07A", 7},
		{"TUI-07B.md", "TUI-07B", 7},
		{"TUI-07C.md", "TUI-07C", 7},
		{"T3.284.md", "T3.284", 284},
	} {
		c, err := ParseCard(tc.file, []byte(validCard))
		if err != nil {
			t.Fatalf("ParseCard(%s): %v", tc.file, err)
		}
		if c.ID != tc.want || c.Number != tc.number || !IsID(tc.want) {
			t.Errorf("%s: ID=%q Number=%d IsID=%v", tc.file, c.ID, c.Number, IsID(tc.want))
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

func TestLoadCardsAndReadTaskEventsUseFileAdapter(t *testing.T) {
	previous := observe.Default()
	defer observe.SetDefault(previous)

	m := observe.NewMetrics()
	observe.SetDefault(observe.NewRecorder(nil, m))

	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
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
	if len(cards) != 2 {
		t.Fatalf("карточек = %d, ждали 2", len(cards))
	}
	if got := counterValue(t, m, observe.CounterFileAdapterReads); got != 2 {
		t.Errorf("%s = %d, ждали 2 прочитанные карточки", observe.CounterFileAdapterReads, got)
	}

	if _, err := ReadTaskEvents(filepath.Join(dir, "T1.1.md")); err != nil {
		t.Fatalf("ReadTaskEvents: %v", err)
	}
	if got := counterValue(t, m, observe.CounterFileAdapterReads); got != 3 {
		t.Errorf("%s = %d, ждали 3 после ReadTaskEvents", observe.CounterFileAdapterReads, got)
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

func TestMeasureIDCollisions(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	third := t.TempDir()
	for _, root := range []string{first, second, third} {
		if err := os.MkdirAll(filepath.Join(root, "docs", "tasks"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeCard := func(root, name, content string) {
		t.Helper()
		p := filepath.Join(root, "docs", "tasks", name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeCard(first, "T2.1.md", "одинаково\n")
	writeCard(second, "T2.1.md", "одинаково\n")
	writeCard(first, "T2.2.md", "слева\n")
	writeCard(second, "T2.2.md", "справа\n")
	writeCard(third, "T3.1.md", "только здесь\n")
	writeCard(first, "notes.md", "не карточка\n")

	report, err := MeasureIDCollisions(first, second, third, filepath.Join(third, "пусто"))
	if err != nil {
		t.Fatalf("MeasureIDCollisions: %v", err)
	}
	if report.Worktrees != 3 {
		t.Errorf("Worktrees = %d, ждали 3", report.Worktrees)
	}
	if report.Cards != 5 {
		t.Errorf("Cards = %d, ждали 5", report.Cards)
	}
	if report.Duplicates != 2 {
		t.Errorf("Duplicates = %d, ждали 2", report.Duplicates)
	}
	if len(report.Collisions) != 1 || report.Collisions[0].ID != "T2.2" || len(report.Collisions[0].Roots) != 2 {
		t.Fatalf("Collisions = %+v, ждали один T2.2 в двух корнях", report.Collisions)
	}
	if !IsID("T2.2") {
		t.Fatal("IsID(T2.2) = false")
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

func TestParseLinkageCardIDShapes(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{"letter after dash", "fix(atx): guard (ATX-R01)\n\nСсылка на ADR 0295.", "ATX-R01"},
		{"compound prefix", "fix(rs): compare (RS-CMP-06)\n\nСсылка на ADR 0295.", "RS-CMP-06"},
		{"dash digits", "fix(store): legacy (HD-022)\n\nСсылка на ADR 0295.", "HD-022"},
		{"dot id", "fix(store): legacy (T3.17)\n\nСсылка на ADR 0295.", "T3.17"},
		{"digit dash letter", "fix(tui): suffix (TUI-07A)\n\nСсылка на ADR 0295.", "TUI-07A"},
		{"adr ref skipped", "fix(atx): only adr\n\nСсылка на ADR-0295.", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseLinkage(c.msg).CardRef; got != c.want {
				t.Errorf("CardRef = %q, ждали %q", got, c.want)
			}
		})
	}
}

func TestParseLinkageExemptWithoutCard(t *testing.T) {
	msg := "fix(atx): temporary hook\n\nСсылка на ADR 0295.\nОбоснование: внешний инструмент без карточки"
	l := ParseLinkage(msg)
	if l.CardRef != "" {
		t.Errorf("CardRef = %q, ждали пустую", l.CardRef)
	}
	if got := CheckLinkage(l, LinkagePorts{}); got != LinkageExempted {
		t.Errorf("CheckLinkage = %q, ждали %q", got, LinkageExempted)
	}
}

func TestParseLinkageNewCardFormatsLinked(t *testing.T) {
	ports := LinkagePorts{
		ValidADR:  func(ref string) bool { return ref == "ADR 0295" },
		ValidCard: IsID,
	}
	cases := []struct {
		name string
		msg  string
	}{
		{"ATX-R01", "fix(atx): guard (ATX-R01)\n\nСсылка на ADR 0295."},
		{"RS-CMP-06", "fix(rs): compare (RS-CMP-06)\n\nСсылка на ADR 0295."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := ParseLinkage(c.msg)
			if !IsID(l.CardRef) {
				t.Fatalf("CardRef = %q, не валидный ID", l.CardRef)
			}
			if got := CheckLinkage(l, ports); got != LinkageLinked {
				t.Errorf("CheckLinkage = %q, ждали %q", got, LinkageLinked)
			}
		})
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

func TestAdaptiveIDLength(t *testing.T) {
	cfg := DefaultIDConfig()
	prob := func(n, length int) float64 {
		total := math.Pow(float64(cfg.Base), float64(length))
		return 1.0 - math.Exp(-float64(n*n)/(2.0*total))
	}
	for _, n := range []int{100, 1000, 10000} {
		got := AdaptiveIDLength(n, cfg)
		if got < cfg.MinLength || got > cfg.MaxLength {
			t.Fatalf("AdaptiveIDLength(%d) = %d вне диапазона", n, got)
		}
		if prob(n, got) > cfg.CollisionThreshold {
			t.Errorf("AdaptiveIDLength(%d) = %d, вероятность %.4f выше порога %.2f", n, got, prob(n, got), cfg.CollisionThreshold)
		}
		if got > cfg.MinLength && prob(n, got-1) <= cfg.CollisionThreshold {
			t.Errorf("AdaptiveIDLength(%d) = %d не минимальна: длина %d уже даёт %.4f", n, got, got-1, prob(n, got-1))
		}
	}
	if AdaptiveIDLength(100, DefaultIDConfig()) != 3 {
		t.Fatal("AdaptiveIDLength(100) должен быть 3")
	}
	if AdaptiveIDLength(1000, DefaultIDConfig()) != 5 {
		t.Fatal("AdaptiveIDLength(1000) должен быть 5")
	}
	if AdaptiveIDLength(10000, DefaultIDConfig()) != 6 {
		t.Fatal("AdaptiveIDLength(10000) должен быть 6")
	}

	strict := DefaultIDConfig()
	strict.CollisionThreshold = 0.001
	if got := AdaptiveIDLength(1000, strict); got <= AdaptiveIDLength(1000, cfg) {
		t.Errorf("более строгий порог не увеличил длину: %d", got)
	}
}

func TestMintBatchUniqueAndReadable(t *testing.T) {
	cfg := DefaultIDConfig()
	seeds := make([]string, 100)
	for i := range seeds {
		seeds[i] = "одинаковый заголовок"
	}
	ids, err := MintBatch(3, len(seeds), seeds, cfg)
	if err != nil {
		t.Fatalf("MintBatch: %v", err)
	}
	if len(ids) != 100 {
		t.Fatalf("MintBatch вернул %d ID, ждали 100", len(ids))
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("повторный ID в пачке: %s", id)
		}
		seen[id] = true
		if !IsID(id) || !hashIDRE.MatchString(id) {
			t.Errorf("MintBatch выдал нечитаемый ID %q", id)
		}
	}
	card, err := ParseCard(filepath.Join(t.TempDir(), ids[0]+".md"), []byte(validCard))
	if err != nil {
		t.Fatalf("ParseCard(%s): %v", ids[0], err)
	}
	if card.ID != ids[0] || card.Group != 3 || card.Number != 1 {
		t.Fatalf("ParseCard = %q, group %d, number %d", card.ID, card.Group, card.Number)
	}
}

func TestMintIDUsesNonce(t *testing.T) {
	cfg := DefaultIDConfig()
	claimed := make(map[string]bool)
	first, err := MintID(3, 17, 100, "одинаковые поля", claimed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MintID(3, 17, 100, "одинаковые поля", claimed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("nonce не разделил одинаковые карточки: %s", first)
	}
	if !IsID(first) || !IsID(second) {
		t.Fatalf("нечитаемые ID: %s, %s", first, second)
	}
}

func TestLegacyIDsStillParse(t *testing.T) {
	for _, id := range []string{"T3.17", "T5.1", "TUI-07A", "ATX-R01", "BS.1", "RS-CMP-06"} {
		if !IsID(id) {
			t.Errorf("IsID(%q) = false", id)
		}
	}
	card, err := ParseCard(filepath.Join(t.TempDir(), "T3.17.md"), []byte(validCard))
	if err != nil {
		t.Fatalf("ParseCard(T3.17): %v", err)
	}
	if card.ID != "T3.17" || card.Group != 3 || card.Number != 17 {
		t.Fatalf("T3.17 = %q, group %d, number %d", card.ID, card.Group, card.Number)
	}
}

func writeMergeCard(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func snapshotCards(t *testing.T, root string) map[string]string {
	t.Helper()
	dir := filepath.Join(root, "docs", "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(data)
	}
	return out
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// mergeCardBody возвращает карточку со всеми обязательными полями. MergeTrees
// читает карточки через ParseCard, поэтому тестовые файлы должны проходить
// обычную валидацию пакета.
func mergeCardBody(title, deps string) string {
	return fmt.Sprintf(`# %s
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/taskstore/store.go
Зависимости: %s
Ссылка: ADR 0003
Цель: цель
Контекст: контекст
Шаги: шаги
Проверка: проверка
Приёмка: приёмка
Агент: нет
Время: нет
`, title, deps)
}

func TestReplaceIDTokenKeepsLongerIDs(t *testing.T) {
	text := "# T3.17. Заголовок\nСсылка: T3.17.k9x2 и T3.17\n"
	got := replaceIDToken(text, "T3.17", "T3.17.abc")
	want := "# T3.17.abc. Заголовок\nСсылка: T3.17.k9x2 и T3.17.abc\n"
	if got != want {
		t.Fatalf("replaceIDToken = %q, ждали %q", got, want)
	}
	if idRoot("T3.17") != "T3" || idRoot("T3.17.k9x2") != "T3" || idRoot("ATX-04") != "ATX" {
		t.Fatal("idRoot вернул неожиданное семейство")
	}
}

func TestMergeTreesResolvesDuplicateID(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	leftBody := mergeCardBody("T3.17. Левая карточка", "T3.20")
	leftDep := mergeCardBody("T3.20. Левая зависимость", "T3.17")
	rightBody := mergeCardBody("T3.17. Правая карточка", "T3.30")
	rightDep := mergeCardBody("T3.30. Правая зависимость", "T3.17")
	writeMergeCard(t, left, "T3.17.md", leftBody)
	writeMergeCard(t, left, "T3.20.md", leftDep)
	writeMergeCard(t, right, "T3.17.md", rightBody)
	writeMergeCard(t, right, "T3.30.md", rightDep)

	report, err := MergeTrees([]string{left, right}, DefaultIDConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Roots != 2 || report.Cards != 4 {
		t.Fatalf("Roots=%d Cards=%d, ждали 2 и 4", report.Roots, report.Cards)
	}
	if report.Conflicts != 1 {
		t.Fatalf("Conflicts=%d, ждали 1", report.Conflicts)
	}
	if len(report.Mappings) != 1 {
		t.Fatalf("Mappings=%+v, ждали одну запись", report.Mappings)
	}
	m := report.Mappings[0]
	if m.OldID != "T3.17" || m.Root != right {
		t.Fatalf("Mapping=%+v, ждали T3.17 в правом корне", m)
	}
	if !hashIDRE.MatchString(m.NewID) || !strings.HasPrefix(m.NewID, "T3.17.") || !IsID(m.NewID) {
		t.Fatalf("NewID=%q, ждали форму T3.17.<base36>", m.NewID)
	}
	if report.DanglingRefs != 0 {
		t.Fatalf("DanglingRefs=%d, ждали 0", report.DanglingRefs)
	}
	if report.DryRun {
		t.Fatal("DryRun=true в обычном слиянии")
	}

	leftFiles := snapshotCards(t, left)
	if len(leftFiles) != 2 || leftFiles["T3.17.md"] != leftBody || leftFiles["T3.20.md"] != leftDep {
		t.Fatalf("победивший корень изменился: %+v", leftFiles)
	}

	rightFiles := snapshotCards(t, right)
	if _, ok := rightFiles["T3.17.md"]; ok {
		t.Fatal("старый файл T3.17.md остался в правом корне")
	}
	newBody, ok := rightFiles[m.NewID+".md"]
	if !ok {
		t.Fatalf("новый файл %s.md не создан", m.NewID)
	}
	if !strings.Contains(newBody, m.NewID) || strings.Contains(newBody, "Зависимости: T3.17\n") {
		t.Fatalf("ссылки в переименованной карточке не переписаны: %q", newBody)
	}
	if got := rightFiles["T3.30.md"]; !strings.Contains(got, "Зависимости: "+m.NewID+"\n") {
		t.Fatalf("ссылка в T3.30.md не переписана: %q", got)
	}

	after, err := MeasureIDCollisions(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if after.Duplicates != 0 {
		t.Fatalf("Duplicates=%d после слияния, ждали 0", after.Duplicates)
	}
}

func TestMergeTreesDryRunLeavesFilesUntouched(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	writeMergeCard(t, left, "T3.17.md", mergeCardBody("T3.17. Левая", ""))
	writeMergeCard(t, right, "T3.17.md", mergeCardBody("T3.17. Правая", ""))
	beforeLeft, beforeRight := snapshotCards(t, left), snapshotCards(t, right)

	report, err := MergeTrees([]string{left, right}, DefaultIDConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.Conflicts != 1 || len(report.Mappings) != 1 {
		t.Fatalf("report=%+v", report)
	}
	if got := snapshotCards(t, left); !sameSnapshot(got, beforeLeft) {
		t.Fatalf("dry-run изменил левый корень: %+v", got)
	}
	if got := snapshotCards(t, right); !sameSnapshot(got, beforeRight) {
		t.Fatalf("dry-run изменил правый корень: %+v", got)
	}
}

func TestMergeTreesIdempotent(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	writeMergeCard(t, left, "T3.17.md", mergeCardBody("T3.17. Левая", ""))
	writeMergeCard(t, right, "T3.17.md", mergeCardBody("T3.17. Правая", "T3.17"))

	first, err := MergeTrees([]string{left, right}, DefaultIDConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Conflicts != 1 || len(first.Mappings) != 1 {
		t.Fatalf("первый проход: %+v", first)
	}
	before := snapshotCards(t, right)
	second, err := MergeTrees([]string{left, right}, DefaultIDConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Conflicts != 0 || len(second.Mappings) != 0 {
		t.Fatalf("второй проход создал новую карту: %+v", second)
	}
	if got := snapshotCards(t, right); !sameSnapshot(got, before) {
		t.Fatalf("второй проход изменил файлы: %+v", got)
	}
}

func TestMergeTreesUniqueIDsDoNotMigrate(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	leftBody := mergeCardBody("T3.17. Левая", "")
	rightBody := mergeCardBody("T3.18. Правая", "")
	writeMergeCard(t, left, "T3.17.md", leftBody)
	writeMergeCard(t, right, "T3.18.md", rightBody)

	report, err := MergeTrees([]string{left, right}, DefaultIDConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Conflicts != 0 || len(report.Mappings) != 0 {
		t.Fatalf("уникальные ID мигрировали: %+v", report)
	}
	if got := snapshotCards(t, left); len(got) != 1 || got["T3.17.md"] != leftBody {
		t.Fatalf("левый корень изменился: %+v", got)
	}
	if got := snapshotCards(t, right); len(got) != 1 || got["T3.18.md"] != rightBody {
		t.Fatalf("правый корень изменился: %+v", got)
	}
}
