package taskstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func openTestIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := OpenIndex(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// indexTables возвращает имена таблиц индекса.
func indexTables(t *testing.T, idx *Index) []string {
	t.Helper()
	rows, err := idx.db.Query(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name IN ('cards', 'deps', 'owners')
		ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return names
}

// indexSnapshot возвращает все строки индекса в устойчивом порядке. Два
// снимка равны тогда, когда повторная сборка не изменила данные.
func indexSnapshot(t *testing.T, idx *Index) []string {
	t.Helper()
	queries := []string{
		`SELECT 'cards|' || id || '|' || status || '|' || raw_sha256 FROM cards ORDER BY id`,
		`SELECT 'deps|' || card_id || '|' || dep FROM deps ORDER BY card_id, dep`,
		`SELECT 'owners|' || card_id || '|' || owner FROM owners ORDER BY card_id, owner`,
	}
	var out []string
	for _, q := range queries {
		rows, err := idx.db.Query(q)
		if err != nil {
			t.Fatalf("snapshot query: %v", err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("snapshot scan: %v", err)
			}
			out = append(out, line)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			t.Fatalf("snapshot rows: %v", rowsErr)
		}
	}
	sort.Strings(out)
	return out
}

func sampleCards() []Card {
	return []Card{
		{
			ID:           "BS.1",
			Type:         "task",
			Status:       "done",
			Phase:        "P0",
			OwnerFiles:   []string{"cmd/a.go", "cmd/b.go"},
			Dependencies: []string{"BS.0"},
			Path:         "docs/tasks/BS.1.md",
			Raw:          "raw-bs1",
			Group:        0,
			Number:       1,
		},
		{
			ID:           "T3.7",
			Type:         "task",
			Status:       "todo",
			Phase:        "P3",
			Dependencies: []string{"T3.1", "T3.2"},
			OwnerFiles:   []string{"internal/x.go"},
			Path:         "docs/tasks/T3.7.md",
			Raw:          "raw-t37",
			Group:        3,
			Number:       7,
		},
	}
}

func TestIndexPathUsesStateDirAndRootHash(t *testing.T) {
	got := IndexPath(filepath.Join("state", "beacon"), "/repo")
	if filepath.Dir(got) != filepath.Join("state", "beacon") {
		t.Fatalf("индекс вне state dir: %s", got)
	}
	if !strings.Contains(filepath.Base(got), Hash8("/repo")) {
		t.Fatalf("в имени индекса нет хеша проекта: %s", got)
	}
}

func TestOpenIndexCreatesThreeTables(t *testing.T) {
	idx := openTestIndex(t)
	if got, want := indexTables(t, idx), []string{"cards", "deps", "owners"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("таблицы = %v, ждали %v", got, want)
	}
}

func TestImportCardsWritesRows(t *testing.T) {
	idx := openTestIndex(t)
	stats, err := idx.ImportCards(sampleCards())
	if err != nil {
		t.Fatalf("ImportCards: %v", err)
	}
	if want := (ImportStats{Cards: 2, Deps: 3, Owners: 3}); stats != want {
		t.Fatalf("stats = %+v, ждали %+v", stats, want)
	}

	var status, sum string
	err = idx.db.QueryRow(`SELECT status, raw_sha256 FROM cards WHERE id = ?`, "BS.1").Scan(&status, &sum)
	if err != nil {
		t.Fatalf("карточка BS.1 не найдена: %v", err)
	}
	if status != "done" || sum != sha256sum("raw-bs1") {
		t.Fatalf("BS.1: status=%q sum=%q", status, sum)
	}

	var deps, owners int
	if err := idx.db.QueryRow(`SELECT count(*) FROM deps WHERE card_id = ?`, "T3.7").Scan(&deps); err != nil {
		t.Fatalf("deps T3.7: %v", err)
	}
	if err := idx.db.QueryRow(`SELECT count(*) FROM owners WHERE card_id = ?`, "T3.7").Scan(&owners); err != nil {
		t.Fatalf("owners T3.7: %v", err)
	}
	if deps != 2 || owners != 1 {
		t.Fatalf("T3.7: deps=%d owners=%d", deps, owners)
	}
}

func TestImportCardsIdempotent(t *testing.T) {
	idx := openTestIndex(t)
	cards := sampleCards()
	first, err := idx.ImportCards(cards)
	if err != nil {
		t.Fatalf("первый импорт: %v", err)
	}
	before := indexSnapshot(t, idx)

	second, err := idx.ImportCards(cards)
	if err != nil {
		t.Fatalf("повторный импорт: %v", err)
	}
	after := indexSnapshot(t, idx)

	if first != second {
		t.Fatalf("stats изменились: %+v -> %+v", first, second)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("повтор изменил данные:\nдо:  %v\nпосле: %v", before, after)
	}
}

func TestImportCardsDeduplicatesTokens(t *testing.T) {
	idx := openTestIndex(t)
	cards := sampleCards()
	cards[0].Dependencies = []string{"BS.0", "BS.0", " BS.1 "}
	cards[0].OwnerFiles = []string{"cmd/a.go", "cmd/a.go"}
	stats, err := idx.ImportCards(cards)
	if err != nil {
		t.Fatalf("ImportCards с повторами: %v", err)
	}
	if want := (ImportStats{Cards: 2, Deps: 4, Owners: 2}); stats != want {
		t.Fatalf("stats = %+v, ждали %+v", stats, want)
	}
}

func TestImportCardsReflectsSourceOfTruth(t *testing.T) {
	idx := openTestIndex(t)
	if _, err := idx.ImportCards(sampleCards()); err != nil {
		t.Fatalf("первый импорт: %v", err)
	}
	if _, err := idx.ImportCards(sampleCards()[:1]); err != nil {
		t.Fatalf("второй импорт: %v", err)
	}
	stats, err := idx.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if want := (ImportStats{Cards: 1, Deps: 1, Owners: 2}); stats != want {
		t.Fatalf("stats = %+v, ждали %+v", stats, want)
	}
	var n int
	if err := idx.db.QueryRow(`SELECT count(*) FROM deps WHERE card_id = ?`, "T3.7").Scan(&n); err != nil {
		t.Fatalf("устаревшие зависимости не удалены: %v", err)
	}
	if n != 0 {
		t.Fatalf("устаревшие зависимости карточки T3.7 остались: %d", n)
	}
}

const indexTestCard = `# %s. Карточка для индекса
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: %s
Зависимости: %s
Ссылка: ADR 0001
Агент: нет
Время: нет
Цель: цель
Контекст: контекст
Шаги: шаги
Проверка: проверка
Приёмка: приёмка
`

func TestRebuildDirParsesMarkdown(t *testing.T) {
	dir := t.TempDir()
	first := fmt.Sprintf(indexTestCard, "T1.1", "internal/a.go, internal/b.go", "T0.1")
	second := fmt.Sprintf(indexTestCard, "T1.2", "internal/c.go", "нет")
	if err := os.WriteFile(filepath.Join(dir, "T1.1.md"), []byte(first), 0644); err != nil {
		t.Fatalf("запись T1.1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "T1.2.md"), []byte(second), 0644); err != nil {
		t.Fatalf("запись T1.2: %v", err)
	}

	idx := openTestIndex(t)
	stats, err := idx.RebuildDir(dir)
	if err != nil {
		t.Fatalf("RebuildDir: %v", err)
	}
	if want := (ImportStats{Cards: 2, Deps: 1, Owners: 3}); stats != want {
		t.Fatalf("stats = %+v, ждали %+v", stats, want)
	}
	var dep string
	if err := idx.db.QueryRow(`SELECT dep FROM deps WHERE card_id = ?`, "T1.1").Scan(&dep); err != nil {
		t.Fatalf("зависимость из markdown не импортирована: %v", err)
	}
	if dep != "T0.1" {
		t.Fatalf("dep = %q, ждали T0.1", dep)
	}
}

// scanCardCorpus читает карточки напрямую и считает токены без ParseCard.
// Счётчики служат независимым oracle для проверки реального корпуса.
func scanCardCorpus(t *testing.T, dir string) ImportStats {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("каталог карточек: %v", err)
	}
	tokens := func(value string) []string {
		value = strings.TrimSpace(value)
		if value == "" || value == "нет" {
			return nil
		}
		seen := make(map[string]bool)
		var out []string
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" && !seen[part] {
				seen[part] = true
				out = append(out, part)
			}
		}
		return out
	}
	var stats ImportStats
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("чтение %s: %v", e.Name(), err)
		}
		stats.Cards++
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "Зависимости:"); ok {
				stats.Deps += len(tokens(v))
			}
			if v, ok := strings.CutPrefix(line, "Владелец файлов:"); ok {
				stats.Owners += len(tokens(v))
			}
		}
	}
	return stats
}

func writeIndexCards(t *testing.T, dir string) {
	t.Helper()
	first := fmt.Sprintf(indexTestCard, "T1.1", "internal/a.go, internal/b.go", "T0.1")
	second := fmt.Sprintf(indexTestCard, "T1.2", "internal/c.go", "нет")
	if err := os.WriteFile(filepath.Join(dir, "T1.1.md"), []byte(first), 0644); err != nil {
		t.Fatalf("запись T1.1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "T1.2.md"), []byte(second), 0644); err != nil {
		t.Fatalf("запись T1.2: %v", err)
	}
}

// indexChanges возвращает число строк, изменённых соединением индекса.
// Повторная сборка без изменений не должна увеличивать этот счётчик.
func indexChanges(t *testing.T, idx *Index) int {
	t.Helper()
	var n int
	if err := idx.db.QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatalf("total_changes: %v", err)
	}
	return n
}

func TestRebuildDirSkipsUnchangedCards(t *testing.T) {
	dir := t.TempDir()
	writeIndexCards(t, dir)

	idx := openTestIndex(t)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("первый RebuildDir: %v", err)
	}
	before := indexChanges(t, idx)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("повторный RebuildDir: %v", err)
	}
	if after := indexChanges(t, idx); after != before {
		t.Fatalf("неизменившиеся карточки перезаписаны: total_changes %d -> %d", before, after)
	}
}

func TestRebuildDirWritesOnlyChangedCard(t *testing.T) {
	dir := t.TempDir()
	writeIndexCards(t, dir)

	idx := openTestIndex(t)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("первый RebuildDir: %v", err)
	}

	cards, err := LoadCards(dir)
	if err != nil {
		t.Fatalf("LoadCards: %v", err)
	}
	fullStart := indexChanges(t, idx)
	if _, err := idx.ImportCards(cards); err != nil {
		t.Fatalf("полная пересборка: %v", err)
	}
	fullDelta := indexChanges(t, idx) - fullStart

	path := filepath.Join(dir, "T1.1.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение T1.1: %v", err)
	}
	changed := strings.Replace(string(data), "Статус: todo", "Статус: in_progress", 1)
	if changed == string(data) {
		t.Fatal("в T1.1 не найдено поле Статус")
	}
	if err := os.WriteFile(path, []byte(changed), 0644); err != nil {
		t.Fatalf("запись T1.1: %v", err)
	}

	var untouchedStatus, untouchedSum string
	if err := idx.db.QueryRow(`SELECT status, raw_sha256 FROM cards WHERE id = ?`, "T1.2").
		Scan(&untouchedStatus, &untouchedSum); err != nil {
		t.Fatalf("T1.2 до обновления: %v", err)
	}

	incStart := indexChanges(t, idx)
	stats, err := idx.RebuildDir(dir)
	if err != nil {
		t.Fatalf("инкрементальный RebuildDir: %v", err)
	}
	incDelta := indexChanges(t, idx) - incStart
	if incDelta <= 0 {
		t.Fatal("изменившаяся карточка не записана")
	}
	if incDelta >= fullDelta {
		t.Fatalf("записано слишком много строк: инкремент %d, полная сборка %d", incDelta, fullDelta)
	}
	if want := (ImportStats{Cards: 2, Deps: 1, Owners: 3}); stats != want {
		t.Fatalf("stats = %+v, ждали %+v", stats, want)
	}

	var status, sum string
	if err := idx.db.QueryRow(`SELECT status, raw_sha256 FROM cards WHERE id = ?`, "T1.1").
		Scan(&status, &sum); err != nil {
		t.Fatalf("T1.1 после обновления: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("статус T1.1 = %q, ждали in_progress", status)
	}
	if err := idx.db.QueryRow(`SELECT status, raw_sha256 FROM cards WHERE id = ?`, "T1.2").
		Scan(&status, &sum); err != nil {
		t.Fatalf("T1.2 после обновления: %v", err)
	}
	if status != untouchedStatus || sum != untouchedSum {
		t.Fatalf("неизменившаяся карточка T1.2 перезаписана: %q/%s -> %q/%s",
			untouchedStatus, untouchedSum, status, sum)
	}
}

func TestRebuildDirRemovesDeletedCards(t *testing.T) {
	dir := t.TempDir()
	writeIndexCards(t, dir)

	idx := openTestIndex(t)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("первый RebuildDir: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "T1.2.md")); err != nil {
		t.Fatalf("удаление T1.2: %v", err)
	}

	stats, err := idx.RebuildDir(dir)
	if err != nil {
		t.Fatalf("RebuildDir после удаления: %v", err)
	}
	if want := (ImportStats{Cards: 1, Deps: 1, Owners: 2}); stats != want {
		t.Fatalf("stats = %+v, ждали %+v", stats, want)
	}
	var n int
	if err := idx.db.QueryRow(`SELECT count(*) FROM cards WHERE id = ?`, "T1.2").Scan(&n); err != nil {
		t.Fatalf("проверка T1.2: %v", err)
	}
	if n != 0 {
		t.Fatalf("удалённая карточка осталась в индексе: %d строк", n)
	}
}

func TestRebuildRealCorpus(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "docs", "tasks")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("корпус карточек недоступен: %v", err)
	}
	want := scanCardCorpus(t, dir)
	if want.Cards < 900 {
		t.Fatalf("корпус слишком мал для проверки: %d карточек", want.Cards)
	}

	idx := openTestIndex(t)
	stats, err := idx.RebuildDir(dir)
	if err != nil {
		t.Fatalf("RebuildDir реального корпуса: %v", err)
	}
	if stats != want {
		t.Fatalf("импорт = %+v, ждали %+v", stats, want)
	}
	before := indexSnapshot(t, idx)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("повторный RebuildDir: %v", err)
	}
	if after := indexSnapshot(t, idx); !reflect.DeepEqual(before, after) {
		t.Fatalf("повторная сборка изменила индекс")
	}
}

func TestGraphStatusFreshIndexIsReady(t *testing.T) {
	dir := t.TempDir()
	first := fmt.Sprintf(indexTestCard, "T1.1", "internal/a.go", "нет")
	second := fmt.Sprintf(indexTestCard, "T1.2", "internal/b.go", "нет")
	if err := os.WriteFile(filepath.Join(dir, "T1.1.md"), []byte(first), 0644); err != nil {
		t.Fatalf("запись T1.1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "T1.2.md"), []byte(second), 0644); err != nil {
		t.Fatalf("запись T1.2: %v", err)
	}

	idx := openTestIndex(t)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("RebuildDir: %v", err)
	}
	status, err := idx.GraphStatus(context.Background())
	if err != nil {
		t.Fatalf("GraphStatus: %v", err)
	}
	if !status.Built {
		t.Fatal("индекс с карточками не отмечен как собранный")
	}
	if status.IndexHash == "" || status.IndexHash != status.SourceHash {
		t.Fatalf("свежий индекс: IndexHash=%q SourceHash=%q", status.IndexHash, status.SourceHash)
	}
	if !status.Ready() {
		t.Fatal("свежий индекс не готов")
	}
}

func TestGraphStatusStaleIndexIsNotReady(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "T1.1.md")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(indexTestCard, "T1.1", "internal/a.go", "нет")), 0644); err != nil {
		t.Fatalf("запись карточки: %v", err)
	}
	idx := openTestIndex(t)
	if _, err := idx.RebuildDir(dir); err != nil {
		t.Fatalf("RebuildDir: %v", err)
	}
	before, err := idx.GraphStatus(context.Background())
	if err != nil {
		t.Fatalf("GraphStatus до изменения: %v", err)
	}
	if !before.Ready() {
		t.Fatalf("индекс до изменения не готов: %+v", before)
	}

	changed := fmt.Sprintf(indexTestCard, "T1.1", "internal/a.go, internal/b.go", "нет")
	if err := os.WriteFile(path, []byte(changed), 0644); err != nil {
		t.Fatalf("изменение карточки: %v", err)
	}
	after, err := idx.GraphStatus(context.Background())
	if err != nil {
		t.Fatalf("GraphStatus после изменения: %v", err)
	}
	if !after.Built {
		t.Fatal("индекс после изменения карточки не собран")
	}
	if after.IndexHash != before.IndexHash {
		t.Fatalf("IndexHash изменился без пересборки: %q -> %q", before.IndexHash, after.IndexHash)
	}
	if after.SourceHash == after.IndexHash {
		t.Fatalf("устаревший индекс имеет совпадающие хеши: %q", after.SourceHash)
	}
	if after.Ready() {
		t.Fatal("устаревший индекс отмечен как готовый")
	}
}

func TestGraphStatusEmptyIndexIsNotReady(t *testing.T) {
	idx := openTestIndex(t)
	status, err := idx.GraphStatus(context.Background())
	if err != nil {
		t.Fatalf("GraphStatus: %v", err)
	}
	if status.Built || status.Ready() {
		t.Fatalf("пустой индекс отмечен как готовый: %+v", status)
	}
}

func TestGraphStatusFollowsLastRebuiltDirectory(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	card := fmt.Sprintf(indexTestCard, "T1.1", "internal/a.go", "нет")
	if err := os.WriteFile(filepath.Join(firstDir, "T1.1.md"), []byte(card), 0644); err != nil {
		t.Fatalf("запись первой карточки: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "T1.1.md"), []byte(card+"\n"), 0644); err != nil {
		t.Fatalf("запись второй карточки: %v", err)
	}

	idx := openTestIndex(t)
	if _, err := idx.RebuildDir(firstDir); err != nil {
		t.Fatalf("первый RebuildDir: %v", err)
	}
	status, err := idx.GraphStatus(context.Background())
	if err != nil {
		t.Fatalf("GraphStatus после первой сборки: %v", err)
	}
	if !status.Ready() {
		t.Fatalf("индекс после первой сборки не готов: %+v", status)
	}
	if _, err := idx.RebuildDir(secondDir); err != nil {
		t.Fatalf("второй RebuildDir: %v", err)
	}
	status, err = idx.GraphStatus(context.Background())
	if err != nil {
		t.Fatalf("GraphStatus после второй сборки: %v", err)
	}
	if !status.Ready() {
		t.Fatalf("индекс после второй сборки не готов: %+v", status)
	}
}
