// Package taskstore: пересобираемый SQLite-индекс карточек.
//
// Markdown-файлы остаются источником истины. Индекс в state dir является
// производным артефактом: его можно удалить и собрать заново из карточек.
// Схема состоит из трёх таблиц: cards, deps, owners.
package taskstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

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
  check       TEXT NOT NULL,
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
type Index struct {
	db *sql.DB
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
	Cards  int
	Deps   int
	Owners int
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
		   context, steps, check, acceptance, path, grp, number, raw_sha256)
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

// RebuildDir загружает все карточки каталога через LoadCards и пересобирает
// индекс. LoadCards разбирает каждую карточку через ParseCard, поэтому в индекс
// попадают только корректные карточки.
func (idx *Index) RebuildDir(tasksDir string) (ImportStats, error) {
	cards, err := LoadCards(tasksDir)
	if err != nil {
		return ImportStats{}, err
	}
	return idx.ImportCards(cards)
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

// sha256sum возвращает шестнадцатеричный хеш исходного текста карточки.
// Хеш позволяет внешним проверкам заметить расхождение индекса и markdown.
func sha256sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// uniqueStrings убирает повторы, сохраняя порядок первого вхождения. Повторные
// токены в поле карточки не должны ломать первичный ключ таблиц deps и owners.
func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
