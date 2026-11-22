package taskcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HookFile описывает один файл, который bootstrap создаёт в каталоге хуков.
// Path относительно этого каталога и использует прямые слеши. Executable
// отмечает примеры, которым нужен бит исполнения. CopyFrom задаёт имя другого
// шаблона, содержимое которого пишется в файл. Так CLAUDE.md повторяет AGENTS.md.
type HookFile struct {
	Path       string
	Body       string
	Executable bool
	CopyFrom   string
}

// HookFS описывает часть файловой системы, которая нужна bootstrap. Сервис
// передаёт реализацию на os, тесты подставляют заглушку со счётчиком записей.
type HookFS interface {
	MkdirAll(path string, perm os.FileMode) error
	Stat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Chmod(path string, perm os.FileMode) error
}

// OSHookFS адаптирует операционную систему к HookFS.
type OSHookFS struct{}

func (OSHookFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (OSHookFS) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (OSHookFS) ReadFile(path string) ([]byte, error)         { return os.ReadFile(path) }
func (OSHookFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func (OSHookFS) Chmod(path string, perm os.FileMode) error { return os.Chmod(path, perm) }

// BootstrapHooks создаёт недостающие файлы каталога хуков. Существующие файлы
// он не трогает, поэтому повторный вызов ничего не записывает. Возвращается
// число записанных файлов. Ошибка записи прерывает обход и возвращается вызывающему.
func BootstrapHooks(dir string, fs HookFS, files []HookFile) (int, error) {
	if strings.TrimSpace(dir) == "" {
		return 0, errors.New("empty hooks directory")
	}
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create hooks directory %s: %w", dir, err)
	}

	written := 0
	for _, file := range files {
		rel := filepath.FromSlash(file.Path)
		if rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return written, fmt.Errorf("invalid hook template path %q", file.Path)
		}
		path := filepath.Join(dir, rel)

		if _, err := fs.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return written, fmt.Errorf("stat %s: %w", rel, err)
		}
		if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return written, fmt.Errorf("create parent of %s: %w", rel, err)
		}

		body := []byte(file.Body)
		if file.CopyFrom != "" {
			src := filepath.Join(dir, filepath.FromSlash(file.CopyFrom))
			var err error
			body, err = fs.ReadFile(src)
			if err != nil {
				return written, fmt.Errorf("read %s: %w", file.CopyFrom, err)
			}
		}

		perm := os.FileMode(0o644)
		if file.Executable {
			perm = 0o755
		}
		if err := fs.WriteFile(path, body, perm); err != nil {
			return written, fmt.Errorf("write %s: %w", rel, err)
		}
		written++
		if file.Executable {
			// WriteFile зависит от umask, поэтому бит исполнения ставим отдельно.
			if err := fs.Chmod(path, 0o755); err != nil {
				return written, fmt.Errorf("chmod %s: %w", rel, err)
			}
		}
	}
	return written, nil
}

// DefaultHookFiles возвращает справку и примеры для нового каталога хуков.
// Четыре документа не исполняемые, пять примеров исполняемые.
func DefaultHookFiles() []HookFile {
	return []HookFile{
		{Path: "AGENTS.md", Body: hooksAgentsDoc},
		{Path: "CLAUDE.md", CopyFrom: "AGENTS.md"},
		{Path: "README.md", Body: hooksReadmeDoc},
		{Path: "examples/README.md", Body: hooksExamplesReadmeDoc},
		{Path: "examples/claimed.example", Body: exampleClaimed, Executable: true},
		{Path: "examples/claimed.notify.example", Body: exampleClaimedNotify, Executable: true},
		{Path: "examples/unblocked.example", Body: exampleUnblocked, Executable: true},
		{Path: "examples/closed.example", Body: exampleClosed, Executable: true},
		{Path: "examples/closed.report.example", Body: exampleClosedReport, Executable: true},
	}
}

const hooksAgentsDoc = "# Хуки beacon\n\n" +
	"Каталог сценариев задаётся параметром `HooksDir` в конфигурации. Сценарии запускаются на трёх событиях карточки: `claimed`, `unblocked` и `closed`.\n\n" +
	"Имя файла до первой точки совпадает с событием. Файлы `claimed.example` и `claimed.report.example` относятся к событию `claimed`. Сценарии одного события идут в порядке сортировки имён.\n\n" +
	"Сценарий получает на stdin один JSON-объект с полями `event`, `card_id` и `time`:\n\n" +
	"    {\"event\":\"claimed\",\"card_id\":\"CD-07\",\"time\":\"2026-11-22T10:00:00Z\"}\n\n" +
	"Ошибка сценария пишется в журнал и не меняет статус карточки. Сценарий не меняет карточки напрямую. Для изменения состояния используйте MCP-инструменты Beacon.\n\n" +
	"Чтобы добавить сценарий, скопируйте файл из `examples/` в корень каталога хуков и переименуйте его в `<event>.<suffix>`. Файл должен быть исполняемым. Bootstrap выставляет бит исполнения для файлов из `examples/`.\n\n" +
	"Повторный вызов bootstrap не перезаписывает существующие файлы. Свои сценарии можно править без риска потерять их при обновлении beacon.\n"

const hooksReadmeDoc = "# Каталог хуков\n\n" +
	"Этот каталог появился при первом использовании хуков. Здесь лежат справка и примеры сценариев.\n\n" +
	"Начните с `AGENTS.md`: там описаны события, формат входа и правила имён. `CLAUDE.md` содержит ту же справку.\n\n" +
	"В `examples/` лежат пять исполняемых примеров. Скопируйте нужный в корень каталога хуков и переименуйте в `<event>.<suffix>`, например `claimed.notify`. Имя до первой точки должно совпадать с событием.\n\n" +
	"Файлы, которые вы создали или изменили, bootstrap не трогает.\n"

const hooksExamplesReadmeDoc = "# Примеры хуков\n\n" +
	"Файлы в этом каталоге исполняемые. Скопируйте пример в корень каталога хуков и переименуйте так, чтобы имя до первой точки совпадало с событием.\n\n" +
	"- `claimed.example` пишет факт взятия карточки в журнал.\n" +
	"- `claimed.notify.example` печатает уведомление в stdout.\n" +
	"- `unblocked.example` пишет факт снятия блокировки в журнал.\n" +
	"- `closed.example` добавляет запись о закрытии в журнал.\n" +
	"- `closed.report.example` печатает отчёт о закрытии.\n\n" +
	"Примеры читают JSON из stdin и извлекают `card_id` через `sed`. Замените эту часть на `jq`, если он установлен в вашем окружении.\n"

const exampleClaimed = `#!/bin/sh
# Событие claimed: карточка взята в работу.
set -eu
payload=$(cat)
card_id=$(printf '%s' "$payload" | sed -n 's/.*"card_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
printf '%s claimed\n' "$card_id" >> "${BEACON_HOOK_LOG:-beacon-hooks.log}"
`

const exampleClaimedNotify = `#!/bin/sh
# Событие claimed: уведомление в stdout.
set -eu
payload=$(cat)
card_id=$(printf '%s' "$payload" | sed -n 's/.*"card_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
printf 'Карточка %s взята в работу\n' "$card_id"
`

const exampleUnblocked = `#!/bin/sh
# Событие unblocked: блокировка снята.
set -eu
payload=$(cat)
card_id=$(printf '%s' "$payload" | sed -n 's/.*"card_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
printf '%s unblocked\n' "$card_id" >> "${BEACON_HOOK_LOG:-beacon-hooks.log}"
`

const exampleClosed = `#!/bin/sh
# Событие closed: карточка закрыта.
set -eu
payload=$(cat)
card_id=$(printf '%s' "$payload" | sed -n 's/.*"card_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
printf '%s closed\n' "$card_id" >> "${BEACON_HOOK_LOG:-beacon-hooks.log}"
`

const exampleClosedReport = `#!/bin/sh
# Событие closed: отчёт в stdout.
set -eu
payload=$(cat)
card_id=$(printf '%s' "$payload" | sed -n 's/.*"card_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
printf 'Карточка %s закрыта\n' "$card_id"
`
