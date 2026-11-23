// Package taskstore: пересобираемый SQLite-индекс карточек.
//
// Markdown-файлы остаются источником истины. Индекс в state dir является
// производным артефактом: его можно удалить и собрать заново из карточек.
// Схема состоит из трёх таблиц: cards, deps, owners.
package taskstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/shared"
	_ "modernc.org/sqlite"
)

const indexDriver = "sqlite"

// indexSchemaSQL создаёт схему индекса. Вызов идемпотентен: существующие
// таблицы не трогаются, отсутствующие создаются.
const indexSchemaSQL = `
CREATE TABLE IF NOT EXISTS cards (
  id          TEXT PRIMARY KEY,
  type        TEXT NOT NULL,
  status      TEXT NOT NULL,
  phase       TEXT NOT NULL,
  reference   TEXT NOT NULL,
  agent       TEXT NOT NULL,
  time        TEXT NOT NULL,
  title       TEXT NOT NULL,
  goal        TEXT NOT NULL,
  context     TEXT NOT NULL,
  steps       TEXT NOT NULL,
  check_text  TEXT NOT NULL,
  acceptance  TEXT NOT NULL,
  path        TEXT NOT NULL,
  grp         INTEGER NOT NULL,
  number      INTEGER NOT NULL,
  raw_sha256  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS cards_by_status ON cards(status);
CREATE INDEX IF NOT EXISTS cards_by_phase ON cards(phase);

CREATE TABLE IF NOT EXISTS deps (
  card_id TEXT NOT NULL,
  dep     TEXT NOT NULL,
  PRIMARY KEY (card_id, dep)
);
CREATE INDEX IF NOT EXISTS deps_by_dep ON deps(dep);

CREATE TABLE IF NOT EXISTS owners (
  card_id TEXT NOT NULL,
  owner   TEXT NOT NULL,
  PRIMARY KEY (card_id, owner)
);
CREATE INDEX IF NOT EXISTS owners_by_owner ON owners(owner);
`

// Index это открытый индекс карточек. Все записи идут через одно соединение.
// tasksDir запоминается последней успешной сборкой и нужен GraphStatus, чтобы
// сравнить индекс с текущим каталогом карточек.
type Index struct {
	db       *sql.DB
	tasksDir string
}

// IndexPath возвращает путь файла индекса в state dir. Суффикс от Hash8(root)
// отделяет индексы разных проектов в одном каталоге состояния.
func IndexPath(stateDir, root string) string {
	return filepath.Join(stateDir, "card-index-"+Hash8(root)+".sqlite")
}

// indexDSN задаёт прагмы на каждом соединении modernc.org/sqlite.
func indexDSN(path string) string {
	return "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

// OpenIndex открывает или создаёт индекс по пути path и применяет схему.
func OpenIndex(path string) (*Index, error) {
	if strings.TrimSpace(path) == "" {
		return nil, Validationf("путь индекса не задан")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, Startupf("не создан каталог индекса: %v", err)
	}
	db, err := sql.Open(indexDriver, indexDSN(path))
	if err != nil {
		return nil, Startupf("не открыт индекс %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, Startupf("не доступен индекс %s: %v", path, err)
	}
	if _, err := db.Exec(indexSchemaSQL); err != nil {
		_ = db.Close()
		return nil, Startupf("не создана схема индекса %s: %v", path, err)
	}
	return &Index{db: db}, nil
}

// Close закрывает индекс. Повторное закрытие безопасно.
func (idx *Index) Close() error {
	if idx == nil || idx.db == nil {
		return nil
	}
	return idx.db.Close()
}

// ImportStats это итог одной сборки индекса.
type ImportStats struct {
	Cards    int
	Deps     int
	Owners   int
	Duration time.Duration
}

// ImportCards пересобирает индекс из cards. Все строки удаляются и вставляются
// заново в одной транзакции. Повтор с тем же набором карточек даёт те же строки.
// Исходный текст карточки не меняется.
func (idx *Index) ImportCards(cards []Card) (ImportStats, error) {
	if idx == nil || idx.db == nil {
		return ImportStats{}, Startupf("индекс не открыт")
	}

	tx, err := idx.db.Begin()
	if err != nil {
		return ImportStats{}, Startupf("не начата сборка индекса: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range []string{`DELETE FROM deps`, `DELETE FROM owners`, `DELETE FROM cards`} {
		if _, err := tx.Exec(stmt); err != nil {
			return ImportStats{}, Startupf("не очищена таблица индекса: %v", err)
		}
	}

	cardStmt, err := tx.Prepare(`
		INSERT INTO cards
		  (id, type, status, phase, reference, agent, time, title, goal,
		   context, steps, check_text, acceptance, path, grp, number, raw_sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return ImportStats{}, Startupf("не подготовлена вставка карточек: %v", err)
	}
	defer cardStmt.Close()

	depStmt, err := tx.Prepare(`INSERT INTO deps (card_id, dep) VALUES (?, ?)`)
	if err != nil {
		return ImportStats{}, Startupf("не подготовлена вставка зависимостей: %v", err)
	}
	defer depStmt.Close()

	ownerStmt, err := tx.Prepare(`INSERT INTO owners (card_id, owner) VALUES (?, ?)`)
	if err != nil {
		return ImportStats{}, Startupf("не подготовлена вставка владельцев: %v", err)
	}
	defer ownerStmt.Close()

	var stats ImportStats
	for _, c := range cards {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return ImportStats{}, Validationf("карточка без ID не может быть проиндексирована")
		}
		sum := sha256sum(c.Raw)
		if _, err := cardStmt.Exec(id, c.Type, c.Status, c.Phase, c.Reference,
			c.Agent, c.Time, c.Title, c.Goal, c.Context, c.Steps, c.Check,
			c.Acceptance, c.Path, c.Group, c.Number, sum); err != nil {
			return ImportStats{}, Startupf("не вставлена карточка %s: %v", id, err)
		}
		stats.Cards++

		for _, dep := range uniqueStrings(c.Dependencies) {
			if _, err := depStmt.Exec(id, dep); err != nil {
				return ImportStats{}, Startupf("не вставлена зависимость карточки %s: %v", id, err)
			}
			stats.Deps++
		}
		for _, owner := range uniqueStrings(c.OwnerFiles) {
			if _, err := ownerStmt.Exec(id, owner); err != nil {
				return ImportStats{}, Startupf("не вставлен владелец карточки %s: %v", id, err)
			}
			stats.Owners++
		}
	}

	if err := tx.Commit(); err != nil {
		return ImportStats{}, Startupf("не зафиксирована сборка индекса: %v", err)
	}
	return stats, nil
}

// RebuildDir синхронизирует индекс с каталогом карточек. Карточка с тем же
// raw_sha256, что уже лежит в индексе, не разбирается повторно и не перезаписывается.
// Удалённые файлы убираются из индекса. Возвращаемая статистика описывает всё
// содержимое индекса после синхронизации.
//
// Раньше путь делал DELETE из трёх таблиц и INSERT всех карточек в одной
// транзакции при каждом вызове. Сейчас при неизменившемся корпусе запись в
// индекс не выполняется.
func (idx *Index) RebuildDir(tasksDir string) (ImportStats, error) {
	if idx == nil || idx.db == nil {
		return ImportStats{}, Startupf("индекс не открыт")
	}
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return ImportStats{}, Startupf("каталог карточек %s недоступен: %v", tasksDir, err)
	}

	stored, err := idx.cardHashes()
	if err != nil {
		return ImportStats{}, err
	}

	seen := make(map[string]bool, len(entries))
	changed := make([]pendingCard, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		filePath := filepath.Join(tasksDir, e.Name())
		data, err := fileAdapter.ReadFile(filePath)
		if err != nil {
			return ImportStats{}, Startupf("не прочитана карточка %s: %v", e.Name(), err)
		}
		sum := sha256sum(string(data))
		id := cardIDFromName(e.Name())
		if id != "" && stored[id] == sum {
			seen[id] = true
			continue
		}
		card, err := ParseCard(filePath, data)
		if err != nil {
			return ImportStats{}, err
		}
		changed = append(changed, pendingCard{card: card, sum: sum})
		seen[card.ID] = true
	}

	stale := make([]string, 0)
	for id := range stored {
		if !seen[id] {
			stale = append(stale, id)
		}
	}

	if err := idx.replaceCards(changed, stale); err != nil {
		return ImportStats{}, err
	}
	idx.tasksDir = tasksDir
	return idx.Counts()
}

// RebuildDirTimed вызывает RebuildDir и заполняет Duration в возвращаемой
// статистике. Замер на текущем дереве карточек записан в docs/perf/README.md.
// Отдельный метод сохраняет RebuildDir совместимым с вызывающими кодом, который
// сравнивает ImportStats целиком.
func (idx *Index) RebuildDirTimed(tasksDir string) (ImportStats, error) {
	start := time.Now()
	stats, err := idx.RebuildDir(tasksDir)
	stats.Duration = time.Since(start)
	return stats, err
}

// Counts возвращает число строк в cards, deps и owners. Метод нужен командам
// сборки и тестам, чтобы сверить индекс с исходными карточками.
func (idx *Index) Counts() (ImportStats, error) {
	if idx == nil || idx.db == nil {
		return ImportStats{}, Startupf("индекс не открыт")
	}
	var stats ImportStats
	for _, q := range []struct {
		query  string
		target *int
	}{
		{`SELECT count(*) FROM cards`, &stats.Cards},
		{`SELECT count(*) FROM deps`, &stats.Deps},
		{`SELECT count(*) FROM owners`, &stats.Owners},
	} {
		if err := idx.db.QueryRow(q.query).Scan(q.target); err != nil {
			return ImportStats{}, Startupf("не прочитан счётчик индекса: %v", err)
		}
	}
	return stats, nil
}

// GraphStatus implements the GraphIndexReader port over the index without
// importing the MCP package. IndexHash is the digest of the cards stored in
// the index. SourceHash is the digest of the directory used in the last
// successful rebuild. When that directory is unknown, SourceHash stays empty
// and domain.GraphStatus.Ready remains false.
func (idx *Index) GraphStatus(_ context.Context) (domain.GraphStatus, error) {
	if idx == nil || idx.db == nil {
		return domain.GraphStatus{}, Startupf("индекс не открыт")
	}
	stored, err := idx.cardHashes()
	if err != nil {
		return domain.GraphStatus{}, err
	}
	status := domain.GraphStatus{
		Built:     len(stored) > 0,
		IndexHash: cardSetHash(stored),
	}
	if idx.tasksDir == "" {
		return status, nil
	}
	source, err := dirCardHashes(idx.tasksDir)
	if err != nil {
		return status, err
	}
	status.SourceHash = cardSetHash(source)
	return status, nil
}

// cardSetHash builds a stable digest over id and raw-text hash pairs. Sorting
// by id makes the digest independent of SQLite row order and directory order.
func cardSetHash(hashes map[string]string) string {
	ids := make([]string, 0, len(hashes))
	for id := range hashes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte(":"))
		h.Write([]byte(hashes[id]))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dirCardHashes reads the markdown cards in tasksDir the same way RebuildDir
// does: directories and non-markdown files are ignored and the id comes from
// the file name. A name with no supported id falls back to the file name,
// which is the id ParseCard assigns to such cards.
func dirCardHashes(tasksDir string) (map[string]string, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, Startupf("каталог карточек %s недоступен: %v", tasksDir, err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := fileAdapter.ReadFile(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			return nil, Startupf("не прочитана карточка %s: %v", e.Name(), err)
		}
		id := cardIDFromName(e.Name())
		if id == "" {
			id = strings.TrimSuffix(e.Name(), ".md")
		}
		out[id] = sha256sum(string(data))
	}
	return out, nil
}

// sha256sum возвращает шестнадцатеричный хеш исходного текста карточки.
// Хеш позволяет внешним проверкам заметить расхождение индекса и markdown.
func sha256sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// cardIDFromName выводит ID карточки из имени файла, не разбирая содержимое.
// Пустая строка означает неподдерживаемое имя: в этом случае RebuildDir
// вызывает ParseCard, чтобы вернуть обычную ошибку валидации.
// Набор форм должен совпадать с ParseCard: иначе RebuildDir не использует
// хеш-кэш для карточек вроде TUI-07A и ATX-R01 и разбирает их заново.
func cardIDFromName(name string) string {
	id := strings.TrimSuffix(name, ".md")
	if m := idRE.FindStringSubmatch(id); m != nil {
		return m[1] + m[2] + "." + m[3]
	}
	if dashIDRE.MatchString(id) || letterDashIDRE.MatchString(id) ||
		digitDashLetterIDRE.MatchString(id) || dotIDRE.MatchString(id) {
		return id
	}
	return ""
}

// cardHashes читает сохранённые хеши исходного текста карточек из индекса.
// По этим хешам RebuildDir решает, какие карточки нужно разобрать заново.
func (idx *Index) cardHashes() (map[string]string, error) {
	rows, err := idx.db.Query(`SELECT id, raw_sha256 FROM cards`)
	if err != nil {
		return nil, Startupf("не прочитаны хеши индекса: %v", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, sum string
		if err := rows.Scan(&id, &sum); err != nil {
			return nil, Startupf("не прочитан хеш индекса: %v", err)
		}
		out[id] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, Startupf("не дочитаны хеши индекса: %v", err)
	}
	return out, nil
}

// pendingCard это карточка, которую нужно записать в индекс.
type pendingCard struct {
	card Card
	sum  string
}

// replaceCards применяет точечные изменения в одной транзакции. У каждой
// изменившейся карточки удаляются старые строки deps и owners, затем вставляются
// новые. Идентификаторы из stale соответствуют удалённым файлам и убираются из
// всех трёх таблиц.
func (idx *Index) replaceCards(changed []pendingCard, stale []string) error {
	if len(changed) == 0 && len(stale) == 0 {
		return nil
	}

	tx, err := idx.db.Begin()
	if err != nil {
		return Startupf("не начато обновление индекса: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	delDeps, err := tx.Prepare(`DELETE FROM deps WHERE card_id = ?`)
	if err != nil {
		return Startupf("не подготовлено удаление зависимостей: %v", err)
	}
	defer delDeps.Close()
	delOwners, err := tx.Prepare(`DELETE FROM owners WHERE card_id = ?`)
	if err != nil {
		return Startupf("не подготовлено удаление владельцев: %v", err)
	}
	defer delOwners.Close()
	delCard, err := tx.Prepare(`DELETE FROM cards WHERE id = ?`)
	if err != nil {
		return Startupf("не подготовлено удаление карточки: %v", err)
	}
	defer delCard.Close()

	remove := func(id string) error {
		if _, err := delDeps.Exec(id); err != nil {
			return Startupf("не удалены зависимости карточки %s: %v", id, err)
		}
		if _, err := delOwners.Exec(id); err != nil {
			return Startupf("не удалены владельцы карточки %s: %v", id, err)
		}
		if _, err := delCard.Exec(id); err != nil {
			return Startupf("не удалена карточка %s: %v", id, err)
		}
		return nil
	}
	for _, id := range stale {
		if err := remove(id); err != nil {
			return err
		}
	}

	insCard, err := tx.Prepare(`
		INSERT INTO cards
		  (id, type, status, phase, reference, agent, time, title, goal,
		   context, steps, check_text, acceptance, path, grp, number, raw_sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return Startupf("не подготовлена вставка карточки: %v", err)
	}
	defer insCard.Close()
	insDep, err := tx.Prepare(`INSERT INTO deps (card_id, dep) VALUES (?, ?)`)
	if err != nil {
		return Startupf("не подготовлена вставка зависимости: %v", err)
	}
	defer insDep.Close()
	insOwner, err := tx.Prepare(`INSERT INTO owners (card_id, owner) VALUES (?, ?)`)
	if err != nil {
		return Startupf("не подготовлена вставка владельца: %v", err)
	}
	defer insOwner.Close()

	for _, p := range changed {
		c := p.card
		if err := remove(c.ID); err != nil {
			return err
		}
		if _, err := insCard.Exec(c.ID, c.Type, c.Status, c.Phase, c.Reference,
			c.Agent, c.Time, c.Title, c.Goal, c.Context, c.Steps, c.Check,
			c.Acceptance, c.Path, c.Group, c.Number, p.sum); err != nil {
			return Startupf("не вставлена карточка %s: %v", c.ID, err)
		}
		for _, dep := range uniqueStrings(c.Dependencies) {
			if _, err := insDep.Exec(c.ID, dep); err != nil {
				return Startupf("не вставлена зависимость карточки %s: %v", c.ID, err)
			}
		}
		for _, owner := range uniqueStrings(c.OwnerFiles) {
			if _, err := insOwner.Exec(c.ID, owner); err != nil {
				return Startupf("не вставлен владелец карточки %s: %v", c.ID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return Startupf("не зафиксировано обновление индекса: %v", err)
	}
	return nil
}

// uniqueStrings убирает повторы, сохраняя порядок первого вхождения. Повторные
// токены в поле карточки не должны ломать первичный ключ таблиц deps и owners.
func uniqueStrings(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			trimmed = append(trimmed, v)
		}
	}
	return shared.DistinctBy(trimmed, func(v string) string { return v })
}
