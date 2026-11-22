# Валидация batch-пакетов research-аудитов

Дата проверки: 22 ноября 2026 года.
SHA проверенного `origin/main`: `c2cb623eeaf21af13d2d05ec1739b5bc9fac12e1`.

## Что проверялось

Список файлов получен командой `git ls-tree -r --name-only origin/main -- docs/research/`. Маска `*batch.json` выбрала четыре пакета auditor. Пятый файл `batches/bcx.json` лежит в этом же каталоге и тоже был проверен, хотя под маску не попадает.

Содержимое каждого файла прочитано через `git show origin/main:<path>`. Синтаксис проверен командой `python3 -m json.tool`. Все пять файлов прошли проверку синтаксиса.

Поля сверены с `internal/infra/batchapply/apply.go` на том же SHA. Допустимые значения:

- `type`: `task`, `adr`, `eval`, `doc`.
- `phase`: `P0`, `P1`, `P2`, `P3`, `P4`, `P5`, `P6`, `P7`, `P8`.
- `status`: `todo`, `done`, `dropped`.
- `id`: шаблон `^[A-Za-z0-9][A-Za-z0-9._-]*$`, без подстроки `..` и без символов `/` и `\`.

Пустые значения `type`, `phase` и `status` допустимы. Функции `renderCard` и `fillCardDefaults` подставляют значения по умолчанию.

Проверка `owner_files` считает путь корректным, если он существует в дереве `origin/main`. Три пути из `.gitignore` (`docs/FACTS.md`, `docs/ROADMAP_STATUS.md`, `docs/adrs/README.md`) создаются генераторами и в git не коммитятся (IDX-11). Их отсутствие в дереве не считается ошибкой пакета.

Волной считается группа карточек с одинаковым значением `phase`. Карточки одной волны запускаются параллельно, поэтому один `owner_file` не может принадлежать двум карточкам волны. Проверялись пересечения внутри пакета и между пакетами.

## Результаты

| Пакет | Источник | Карточек | Синтаксис | Поля и `id` | `owner_files` | Пересечения в волне | Итог |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `audit-01-batch.json` | PR #1430 | 12 | OK | OK | все существуют | внутри нет, между пакетами есть | валиден, есть конфликты волн |
| `audit-02-batch.json` | docs/research | 8 | OK | OK | все существуют | внутри нет, между пакетами есть | валиден, есть конфликты волн |
| `audit-03-batch.json` | PR #1429 | 10 | OK | OK | все существуют | внутри нет, между пакетами есть | валиден, есть конфликты волн |
| `audit-04-batch.json` | PR #1431 | 8 | OK | OK | все существуют | внутри нет, между пакетами есть | валиден, есть конфликты волн |
| `batches/bcx.json` | docs/research/batches | 82 | OK | OK | 18 ссылок: 10 на несуществующие файлы, 2 на каталоги, 6 генерируемые | 44 внутрипакетных и 12 межпакетных | проблемы |

Четыре пакета auditor прошли проверку схемы без замечаний. Все значения `type`, `phase`, `status` и `id` лежат в допустимых наборах. Все объявленные `owner_files` существуют в `origin/main`. Внутри каждого из этих пакетов пересечений owner_files нет.

## Конфликты волн между пакетами аудитов

Семь файлов закреплены за карточками разных пакетов в одной фазе. При параллельном запуске два исполнителя получат один файл.

| Фаза | Файл | Карточки |
| --- | --- | --- |
| P0 | `internal/infra/opencode/client.go` | `AUTOSP-02`, `MEM-01` |
| P0 | `internal/infra/opencode/client_test.go` | `AUTOSP-02`, `MEM-01` |
| P0 | `cmd/beacon/watch.go` | `MEM-02`, `TUI-03` |
| P1 | `cmd/beacon/mcp.go` | `AUTOSP-04`, `TUI-04`, `PROTO-03` |
| P1 | `internal/app/taskcli/service.go` | `AUTOSP-06`, `MEM-06` |
| P1 | `internal/app/taskcli/service_test.go` | `AUTOSP-06`, `MEM-06` |
| P1 | `internal/infra/httpapi/server.go` | `TUI-04`, `PROTO-02` |

Дополнительно карточки аудитов пересекаются с `bcx.json` в тех же фазах: `cmd/beacon/mcp.go` (`MEM-04`, `BCX-71`, `BCX-76`, `BCX-77`), `cmd/beacon/main.go` (`TUI-01`, `BCX-21`, `BCX-22`, `BCX-63`, `BCX-81`), `internal/tui/app.go` (`TUI-02`, `BCX-63`), `internal/infra/httpapi/server.go` (`TUI-04`, `PROTO-02`, `BCX-15`), `internal/domain/types.go` (`TUI-10`, `BCX-14`, `BCX-15`, `BCX-45`), `tests/e2e/e2e_test.go` (`PROTO-01`, `BCX-59`), `cmd/beacon/up.go` (`TUI-05`, `BCX-14`).

## Замечания по `batches/bcx.json`

Пакет содержит 82 карточки и проходит проверку схемы, но имеет две группы проблем.

Первая группа: ссылки на пути, которых нет в дереве `origin/main`.

- Шесть ссылок ведут на генерируемые документы (`docs/adrs/README.md` в карточках `BCX-02`, `BCX-03`, `BCX-05`, `BCX-65`, `BCX-70` и `docs/FACTS.md` в `BCX-08`). Это ожидаемое поведение `.gitignore`, исправления не требует.
- Две ссылки ведут на каталоги, а не на файлы: `docs/research/` (`BCX-10`) и `internal/infra/registry/` (`BCX-58`).
- Десять ссылок ведут на несуществующие файлы: `docs/adrs/0091-mcp.md` (`BCX-02`), `tools/beacon-gates/gate/docsgate/docs.go` (`BCX-05`, `BCX-06`), `docs/adrs/0090-honest-delivery-unchanged-ladder.md` (`BCX-06`), `docs/GLOSSARY.md` (`BCX-12`, `BCX-20`), `internal/infra/server/http/error.go` (`BCX-15`), `docs/adrs/0086-mcp-beacon-wake.md` (`BCX-65`), `docs/adrs/0070-*.md` (`BCX-70`), `docs/research/process-leak-mechanism.md` (`BCX-82`).

Вторая группа: 44 пересечения `owner_files` внутри одной волны. По фазам распределение такое: P0 14, P1 22, P2 8. Чаще всего пересекаются `cmd/beacon/main.go`, `internal/infra/mcp/schema.go`, `internal/domain/types.go`, `internal/infra/mcp/contract_test.go` и `docs/adrs/README.md`.

## Вывод

Пакеты PR #1429, #1430, #1431 и `audit-02-batch.json` валидны по схеме `batchapply`. Их `owner_files` не пересекаются внутри пакета, но семь файлов закреплены за карточками разных пакетов в одной волне. Этот конфликт нужно устранить до параллельного запуска исполнителей.

Пакет `batches/bcx.json` требует правки: 12 ссылок на несуществующие файлы и 44 пересечения внутри волн. Исправления описаны в `docs/research/batch-validation-fixes.json`.
