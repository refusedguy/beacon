# Исправление пакета bcx.json для batch-apply

Дата: 22 ноября 2026 года.
Проверенный SHA: `origin/main` = `9becefcca0086917650d757b0151fc33aa4c0bbd`.
Ветка: `fix/bcx-batch-validation`.
Аудит: `python3 docs/research/batches/fix_bcx.py` из корня репозитория. Скрипт читает `origin/main` через `git ls-tree -r --name-only` и возвращает код 1, если есть замечания.

## Правила, по которым проверялся пакет

`internal/infra/batchapply/apply.go` задаёт закрытые множества для полей карточки.

- `type`: `task`, `adr`, `eval`, `doc`.
- `phase`: `P0` до `P8`.
- `status`: `todo`, `done`, `dropped`.
- `id`: шаблон `^[A-Za-z0-9][A-Za-z0-9._-]*$`, без `..`, `/` и `\`.

Пустые `type`, `phase` и `status` допустимы. `renderCard` и `fillCardDefaults` подставляют значения по умолчанию.

apply.go не проверяет существование `owner_files` и не ищет пересечения. Эти правила приходят из `docs/research/BATCH-VALIDATION.md`. Волной считается группа карточек с одинаковым `phase`. Пересечением считается пара карточек одной волны с одним `owner_file`. Аудит считает количество групп `(файл, фаза)` с более чем одной карточкой, как и в отчёте verify-агента. Ссылки на генерируемые документы (`docs/FACTS.md`, `docs/ROADMAP_STATUS.md`, `docs/adrs/README.md`) не считаются ошибкой: они перечислены в `.gitignore`, а apply.go не запрещает их в `owner_files`.

## До и после

| Показатель | До | После |
| --- | --- | --- |
| Карточек | 82 | 82 |
| Ссылок на несуществующие файлы | 10 | 0 |
| Ссылок на каталоги | 2 | 0 |
| Пересечений в волнах | 44 | 0 |
| Пересечений в P0 | 14 | 0 |
| Пересечений в P1 | 22 | 0 |
| Пересечений в P2 | 8 | 0 |
| Карточек `status: dropped` | 0 | 1 |
| Ссылок на генерируемые документы | 7 | 7 |
| Уникальных `owner_file` в пакете | 141 | 141 |

Вывод аудита до правки:

```text
 cards=82 missing=10 directory_refs=2 overlap_groups=44
-- missing (10) --
   BCX-02 | docs/adrs/0091-mcp.md
   BCX-05 | tools/beacon-gates/gate/docsgate/docs.go
   BCX-06 | docs/adrs/0090-honest-delivery-unchanged-ladder.md
   BCX-06 | tools/beacon-gates/gate/docsgate/docs.go
   BCX-12 | docs/GLOSSARY.md
   BCX-15 | internal/infra/server/http/error.go
   BCX-20 | docs/GLOSSARY.md
   BCX-65 | docs/adrs/0086-mcp-beacon-wake.md
   BCX-70 | docs/adrs/0070-*.md
   BCX-82 | docs/research/process-leak-mechanism.md
-- directory (2) --
   BCX-10 | docs/research/
   BCX-58 | internal/infra/registry/
-- overlap (44) --
   (14 групп в P0, 22 группы в P1, 8 групп в P2)
```

Вывод аудита после правки:

```text
 cards=82 missing=0 directory_refs=0 overlap_groups=0
```

## Dropped-карточка

`BCX-10`, задача «Определить статус research-отчётов в git». Политика уже зафиксирована в `origin/main`. Файл `.gitignore` содержит строку `docs/research/` с комментарием «Отчёты исследований: временные снимки суб-агентов, в репозиторий не идут (BCX-10)». Там же написано, что новые отчёты не коммитятся и не показываются в `git status`, а уже закоммиченные остаются в истории. Карточка помечена `status: dropped`, ссылка на каталог убрана, причина записана в `steps` и `acceptance`. Других dropped-карточек нет.

## Переназначенные пути

| Карточка | Было | Стало | Основание |
| --- | --- | --- | --- |
| BCX-02 | `docs/adrs/0091-mcp.md` | `docs/adrs/0095-cli-opencode-api.md` | Реальный ADR с конфликтующим номером. Переименования в истории нет. |
| BCX-05 | `tools/beacon-gates/gate/docsgate/docs.go` | `tools/beacon-gates/docsgate/docs.go` | Файл лежит на один уровень выше. |
| BCX-06 | `docs/adrs/0090-honest-delivery-unchanged-ladder.md` | `docs/adrs/0090-registry-lifecycle.md` | Реальный ADR 0090 в дереве. |
| BCX-06 | `tools/beacon-gates/gate/docsgate/docs.go` | `tools/beacon-gates/docsgate/docs.go` | Файл лежит на один уровень выше. |
| BCX-12 | `docs/GLOSSARY.md` | `GLOSSARY.md` | Файл лежит в корне. |
| BCX-15 | `internal/infra/server/http/error.go` | `internal/infra/httpapi/error_format_test.go` | Пакет CLEAN-02 убрал общий writer. Формат ошибки HTTP API живёт в httpapi. |
| BCX-20 | `docs/GLOSSARY.md` | `GLOSSARY.md` | Файл лежит в корне. |
| BCX-58 | `internal/infra/registry/` | `internal/infra/registry/index.go` | Конкретный файл реестра вместо каталога. |
| BCX-65 | `docs/adrs/0086-mcp-beacon-wake.md` | `docs/adrs/0089-capability-index.md` | Реальный ADR с конфликтующим номером. Дубль 0086 в дереве отсутствует. |
| BCX-70 | `docs/adrs/0070-*.md` | `docs/adrs/0070-universal-wake-adapter.md` | Подстановочная маска заменена конкретным файлом. |
| BCX-82 | `docs/research/process-leak-mechanism.md` | `.opencode/settings.json` | Отчёт не коммитится по BCX-10. Хук PreToolUse живёт в settings.json. |

Ссылки на генерируемые документы оставлены без изменения. В пакете их семь: `docs/adrs/README.md` в BCX-02, BCX-03, BCX-05, BCX-65 и BCX-70, `docs/FACTS.md` в BCX-08, `docs/ROADMAP_STATUS.md` в BCX-04. Отчёт verify-агента называет шесть и не упоминает BCX-04. Все семь путей перечислены в `.gitignore`.

## Сдвинутые фазы

39 карточек перенесены в более позднюю фазу. Карточка с меньшим номером оставалась в исходной фазе, менее приоритетная карточка с большим номером сдвигалась. Для каждой перемещённой карточки выбиралась первая фаза, в которой ни один её `owner_file` не закреплён за другой карточкой. Число карточек по фазам после правки: P0 5, P1 22, P2 20, P3 13, P4 9, P5 8, P6 3, P7 1, P8 1.

```text
BCX-02: P0 -> P4    BCX-05: P1 -> P3    BCX-12: P0 -> P3
BCX-13: P0 -> P3    BCX-15: P1 -> P6    BCX-20: P2 -> P5
BCX-22: P0 -> P3    BCX-26: P1 -> P4    BCX-27: P1 -> P2
BCX-28: P2 -> P6    BCX-32: P1 -> P3    BCX-35: P2 -> P6
BCX-36: P1 -> P4    BCX-37: P1 -> P3    BCX-39: P2 -> P5
BCX-42: P0 -> P2    BCX-44: P1 -> P7    BCX-45: P1 -> P5
BCX-46: P1 -> P4    BCX-49: P1 -> P5    BCX-51: P1 -> P5
BCX-52: P0 -> P4    BCX-54: P0 -> P3    BCX-55: P0 -> P2
BCX-56: P1 -> P2    BCX-58: P1 -> P2    BCX-62: P0 -> P3
BCX-63: P0 -> P5    BCX-64: P1 -> P4    BCX-65: P1 -> P5
BCX-66: P1 -> P3    BCX-69: P2 -> P8    BCX-71: P0 -> P3
BCX-72: P0 -> P3    BCX-75: P1 -> P4    BCX-76: P1 -> P4
BCX-79: P2 -> P3    BCX-80: P2 -> P5    BCX-81: P0 -> P4
```

## Проверки

1. Синтаксис. `python3 -m json.tool docs/research/batches/bcx.json >/dev/null`. Выполнено, код 0, ошибок нет.
2. Перезапуск аудита. `python3 docs/research/batches/fix_bcx.py`. Код 0. Сводка `cards=82 missing=0 directory_refs=0 overlap_groups=0`.
3. Существование файлов. Для каждого из 141 уникального `owner_file`, кроме семи генерируемых, выполнен `git cat-file -e origin/main:<path>`. Все проверки прошли, код 0, отсутствующих нет.
4. Идентификаторы и поля. 82 ID уникальны и подходят под шаблон. Все `type`, `phase` и `status` лежат в допустимых множествах из apply.go. Переназначенных ID нет.
5. Dropped-карточка. `BCX-10` единственная. Её причина подтверждена комментарием `(BCX-10)` в `.gitignore` на `origin/main`. Живой реализации с той же целью в пакете нет.

Локально не запускались `go test ./...`, `go build ./...`, `beacon gates` и `batch-apply`. Проверки остаются за CI и оркестратором.
