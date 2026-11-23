// Package taskstore: атомарная запись статуса закрытия и его причины.
// Файл работает только со стандартной библиотекой и не импортирует соседние
// пакеты. Запись идёт под тем же файловым замком и AtomicWrite, что и Take в
// internal/app/taskcli, поэтому читатель видит либо прежнюю карточку, либо
// полную новую.
package taskstore

import (
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// closeStatuses — статусы, которые пишет CloseCard. todo и in_progress
// остаются в карточке как активные и этим writer не трогаются.
var closeStatuses = []string{"blocked", "done", "dropped", "duplicate"}

// closeReasons — канонические значения поля Причина из обзора закрытия задач.
// CloseCard не отклоняет другие значения: причина хранится в карточке как есть.
var closeReasons = []string{
	"completed", "already_landed", "duplicate", "not_planned", "owner_decision", "obsolete",
}

// IsCloseStatus сообщает, является ли значение статусом закрытия.
func IsCloseStatus(v string) bool { return slices.Contains(closeStatuses, v) }

// CloseCardOptions — данные, которые пишутся в карточку при закрытии.
// Статус и Дубликат проверяются. Время закрытия при пустом значении ставится
// в текущее время UTC в формате RFC3339.
type CloseCardOptions struct {
	Status      string // обязательный: blocked, done, dropped или duplicate
	Reason      string // Причина
	DuplicateOf string // Дубликат, обязателен при Status == "duplicate"
	ClosedBy    string // Закрыл
	ClosedAt    string // Время закрытия, RFC3339 UTC
	Blocker     string // Блокировщик, для blocked
}

// managedCloseFields задаёт порядок полей, которые добавляются в карточку,
// если их ещё нет. Статус уже обязателен в каждой карточке, поэтому он будет
// найден на месте и не вставится заново. AUD-05: этот список и порядок —
// канонический формат закрытия. Адаптер beacon_task_close повторяет его в
// internal/infra/mcp/tool_task_close.go, потому что слои запрещают ему
// импортировать taskstore; менять оба списка надо вместе.
var managedCloseFields = []string{
	"Статус", "Блокировщик", "Причина", "Дубликат", "Закрыл", "Время закрытия",
}

// CloseCard атомарно пишет поля закрытия в карточку под файловым замком.
// lockPath и projectRoot идут в AcquireLock, cardPath — путь файла карточки.
func CloseCard(lockPath, projectRoot, cardPath string, o CloseCardOptions) error {
	lock, err := AcquireLock(lockPath, projectRoot)
	if err != nil {
		return err
	}
	defer lock.Release()

	data, err := fileAdapter.ReadFile(cardPath)
	if err != nil {
		return Startupf("не прочитана карточка %s: %v", cardPath, err)
	}
	if _, err := ParseCard(filepath.Base(cardPath), data); err != nil {
		return err
	}
	newText, err := ApplyCloseFields(string(data), o)
	if err != nil {
		return err
	}
	return AtomicWrite(cardPath, []byte(newText), 0644)
}

// ApplyCloseFields возвращает текст карточки с обновлёнными полями закрытия.
// Чистая функция: не читает и не пишет файлы. Существующие строки заменяются
// на месте, отсутствующие вставляются перед первым разделом «## ». Пустое
// значение удаляет строку поля, поэтому повторная запись не оставляет мусора.
func ApplyCloseFields(raw string, o CloseCardOptions) (string, error) {
	if !IsCloseStatus(o.Status) {
		return "", Validationf("неверный статус закрытия %q", o.Status)
	}
	if o.Status == "duplicate" && strings.TrimSpace(o.DuplicateOf) == "" {
		return "", Validationf("статус duplicate требует поле Дубликат")
	}
	when := strings.TrimSpace(o.ClosedAt)
	if when == "" {
		when = time.Now().UTC().Format(time.RFC3339)
	} else if _, err := time.Parse(time.RFC3339, when); err != nil {
		return "", Validationf("неверное время закрытия %q: %v", o.ClosedAt, err)
	}

	values := map[string]string{
		"Статус":         o.Status,
		"Блокировщик":    strings.TrimSpace(o.Blocker),
		"Причина":        strings.TrimSpace(o.Reason),
		"Дубликат":       strings.TrimSpace(o.DuplicateOf),
		"Закрыл":         strings.TrimSpace(o.ClosedBy),
		"Время закрытия": when,
	}

	cr := ""
	if strings.Contains(raw, "\r\n") {
		cr = "\r"
	}
	lines := strings.Split(raw, "\n")
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(lines)+len(managedCloseFields))
	insertAt := -1
	for _, line := range lines {
		if insertAt < 0 && strings.HasPrefix(line, "## ") {
			insertAt = len(out)
		}
		lineCR := ""
		body := line
		if strings.HasSuffix(line, "\r") {
			lineCR = "\r"
			body = strings.TrimSuffix(line, "\r")
		}
		key, known := managedKey(body)
		if !known {
			out = append(out, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		value := values[key]
		if value == "" {
			continue
		}
		out = append(out, key+": "+value+lineCR)
	}

	missing := make([]string, 0, len(managedCloseFields))
	for _, key := range managedCloseFields {
		if !seen[key] && values[key] != "" {
			missing = append(missing, key+": "+values[key]+cr)
		}
	}
	if len(missing) > 0 {
		if insertAt < 0 {
			insertAt = len(out)
		}
		tail := append([]string(nil), out[insertAt:]...)
		out = append(out[:insertAt:insertAt], missing...)
		out = append(out, tail...)
	}
	return strings.Join(out, "\n"), nil
}

// managedKey возвращает имя поля, если строка — одно из управляемых полей
// закрытия. Сравнение идёт по точному имени, поэтому строки вроде ссылки
// «https://...» не принимаются за поле.
func managedKey(line string) (string, bool) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return "", false
	}
	key := strings.TrimSpace(line[:colon])
	for _, managed := range managedCloseFields {
		if key == managed {
			return managed, true
		}
	}
	return "", false
}
