# Сравнение с Codex: пять приёмов, которых нет в Beacon

Дата: 2026-11-22. Режим: только чтение источников. Код beacon не менялся. `go test`, `go build`, `beacon gates` и `beacon index build` не запускались.

## Методика и ограничения

Репозиторий [openai/codex](https://github.com/openai/codex) склонирован в `/tmp/opencode/codex-research` с глубиной 1. HEAD клона `cf3ff1530d821ec2d26452da4971d8e3e7d18d25`. Сравнение сделано на базе beacon `2d34645b54f45d84dba3a595350f17f8658759da`.

`rg` и `find` применялись только внутри клона. По `/mnt/projects`, `/tmp` и `/home` поиск не шёл. Для beacon использовались `git ls-files`, `git grep` и чтение конкретных файлов. Номера строк сняты с указанных коммитов.

Слово «отсутствует» означает отсутствие механизма в прочитанных файлах. Это не доказательство отсутствия во всём продукте. Карточки ниже фиксируют проверенные разрывы, а не предлагают готовые решения.

## Что прочитано

В Codex: `docs/execpolicy.md`, `docs/sandbox.md`, `docs/agents_md.md`; `codex-rs/core/src/tools/parallel.rs`, `orchestrator.rs`, `multi_agent_tool.rs`, `approvals.rs`, `runtimes/apply_patch.rs`; `codex-rs/execpolicy/src/lib.rs`, `parser.rs`, `sandbox_migration.rs`; `codex-rs/worktree/src/lib.rs`, `metadata.rs`; `codex-rs/apply-patch/src/lib.rs`; `codex-rs/protocol/src/protocol.rs`; `codex-rs/thread-store/src/local/model_context.rs`, `history_revision.rs`; `codex-rs/core/src/rollout.rs`; `codex-rs/exec/tests/suite/resume.rs`; `AGENTS.md` в `codex-rs/code-mode-host`, `codex-rs/exec-server/src/sandbox_integrity` и `codex-rs/rollout-trace`.

В beacon: `AGENTS.md`, `internal/infra/worktree/marker.go`, `internal/infra/worktree/headguard.go`, `internal/infra/batchapply/apply.go`, `internal/app/orchestrator/orchestrator.go`, `cmd/beacon/orchestrate.go`, `cmd/beacon/attach.go`, `cmd/beacon/agents.go`, `docs/adrs/0021-worktree-isolation.md`.

## Таблица разрывов

| ID | Приём Codex | file:line в Codex | Разрыв в beacon | file:line в beacon |
| --- | --- | --- | --- | --- |
| CC-01 | Флаг параллельности инструмента и RwLock на dispatch | `codex-rs/core/src/tools/parallel.rs:223`, `:344-356`, `:323` | Parallel это срез карточек без admission lock и флага способности | `cmd/beacon/orchestrate.go:589-602` |
| CC-02 | Владелец worktree в git-метаданных, atomic no-clobber bind | `codex-rs/worktree/src/metadata.rs:49-85`, `codex-rs/worktree/src/lib.rs:284-317` | Маркер в state dir перезаписывается обычной `os.WriteFile` | `internal/infra/worktree/marker.go:48-68`, `:92-100` |
| CC-03 | Starlark-политика с примерами и дробные одобрения | `codex-rs/execpolicy/src/parser.rs:55-80`, `:135-153`, `:334-394`; `codex-rs/protocol/src/protocol.rs:1232-1247` | Один bool `RequireApproval`, язык правил и кэш по путям отсутствуют | `internal/app/orchestrator/orchestrator.go:72-92` |
| CC-04 | Дельта уже записанных изменений при отказе patch | `codex-rs/apply-patch/src/lib.rs:213-245`, `:278-303`; `codex-rs/core/src/tools/events.rs:424-428` | Пакетная запись файлов без следа частично созданных файлов | `internal/infra/batchapply/apply.go:588-611` |
| CC-05 | Подпись rollout до и после чтения, сброс revision при изменении | `codex-rs/thread-store/src/local/history_revision.rs:9-30`, `codex-rs/thread-store/src/local/model_context.rs:59-94` | Resume берёт sessionID из реестра без проверки изменения состояния | `cmd/beacon/attach.go:225-261`, `AGENTS.md:103-111` |

### CC-01. Параллельность на уровне инструмента

Codex спрашивает у каждого инструмента, можно ли его выполнять вместе с другими. Значение читается перед dispatch в `codex-rs/core/src/tools/parallel.rs:223`. Инструменты с флагом берут чтение `RwLock`, остальные берут запись (`:344-356`). Сам вызов уходит в `tokio::spawn` под `AbortOnDropHandle` (`:323`). Проверка отмены стоит до захвата замка и после ожидания слота (`:333-370`). Параллельные чтения не блокируют друг друга, а инструмент с побочным эффектом получает эксклюзивный доступ.

Beacon планирует параллельную волну как срез карточек в `cmd/beacon/orchestrate.go:589-602`. У отдельного действия нет флага способности к параллелизму. Есть проверка, что «гигант» имеет непересекающийся seam (`:606-612`). Два действия в одном шаге не проходят через admission lock. Отмена одного действия не меняет доступ другого к тем же файлам.

### CC-02. Привязка worktree к потоку

Codex хранит владельца чек-аута в версионированной записи `codex-thread.json` внутри git-метаданных (`codex-rs/worktree/src/metadata.rs:19-27`). `bind_thread` пишет во временный файл и вызывает `persist_noclobber` (`:63-74`). Тот же поток при повторном bind получает успех, другой поток получает ошибку, а гонка двух процессов различается через `AlreadyExists` (`:76-81`). Удаление чек-аута отказывается, если в нём есть локальные изменения (`codex-rs/worktree/src/lib.rs:284-317`). Список чек-аутов дополнительно проверяет backlink admin-директории (`:230-252`).

Beacon пишет маркер в свой state dir, ключом служит хэш очищенного пути (`internal/infra/worktree/marker.go:37-43`). `WriteMarker` вызывает `os.WriteFile` и перезаписывает существующую запись (`:48-68`). `TouchMarker` сохраняет agentID, но тоже перезаписывает файл (`:92-100`). Два агента, начавших работу в одном worktree, оба получают успех. Последняя запись побеждает. Связи с git-метаданными и atomic no-clobber нет.

### CC-03. Политика исполнения и одобрений

Политика Codex это программа на Starlark. `PolicyParser::parse` разбирает `AstModule` и вычисляет его с собственными builtins (`codex-rs/execpolicy/src/parser.rs:55-80`). Builtin `prefix_rule` принимает аргументы `match` и `not_match`, сохраняет примеры и место вызова (`:334-394`). После разбора `validate_pending_examples_from` прогоняет примеры через собранную политику и прикрепляет location к ошибке (`:135-153`). Одноразовая миграция удаляет устаревшие правила и пишет маркер через `persist_noclobber` (`codex-rs/execpolicy/src/sandbox_migration.rs:16-29`).

Одобрения разделены по потокам. `GranularApprovalConfig` содержит флаги `sandbox_approval`, `rules`, `skill_approval`, `request_permissions` и `mcp_elicitations` (`codex-rs/protocol/src/protocol.rs:1232-1247`). Ключ кэша одобрения для patch это пара `environment_id` и `path` (`codex-rs/core/src/tools/approvals.rs:284-321`). После отказа песочницы orchestrator отправляет на одобрение сам retry, а не исходный вызов (`codex-rs/core/src/tools/orchestrator.rs:496-541`). Политика песочницы задана отдельным типом с четырьмя вариантами (`codex-rs/protocol/src/protocol.rs:1293-1329`).

Beacon хранит политику мержа в четырёх полях `AutoMergePolicy`, одобрение это один bool `RequireApproval` (`internal/app/orchestrator/orchestrator.go:72-92`). Языка правил, примеров, миграции и кэша по путям нет. Изоляция агента это `git clone --reference` (`cmd/beacon/agents.go:343-352`). Политики файловой системы и сети и эскалации после отказа в этом механизме нет.

### CC-04. След частично применённого patch

`apply_patch` возвращает не только ошибку, но и `AppliedPatchDelta`: какие файлы уже записаны до отказа (`codex-rs/apply-patch/src/lib.rs:213-245`, `:278-303`). Метод `delta()` доступен вызывающему слою (`:297-303`). Слой событий сохраняет delta даже когда пользователь отклонил patch (`codex-rs/core/src/tools/events.rs:424-428`). Отказ поэтому не теряет след изменений, которые уже попали на диск.

Beacon не вызывает `apply_patch` как инструмент. Генерация документов идёт через `runner.writeFileMode`: функция проверяет отсутствие файла и пишет его `os.WriteFile` (`internal/infra/batchapply/apply.go:588-611`). При ошибке в середине пакета нет delta. Определить, какие файлы уже созданы, из ответа нельзя. Ключа одобрения на путь тоже нет.

### CC-05. Revision fence при возобновлении

Codex снимает подпись состояния rollout до и после чтения: длина, время изменения, время создания и identity `dev/ino/ctime` (`codex-rs/thread-store/src/local/history_revision.rs:9-30`). `load_latest_model_context` возвращает revision только если подписи совпали (`codex-rs/thread-store/src/local/model_context.rs:59-94`). Если файл вырос во время чтения, контекст не переиспользуется. Для fork reverse scan останавливается на ближайшей записи compaction с обоими нужными полями (`:27-34`). Тест `exec_resume_last_appends_to_existing_file` проверяет, что resume дописывает в тот же файл (`codex-rs/exec/tests/suite/resume.rs:154-226`).

Beacon при рестарте читает `git status --short --branch` и ключ `beacon/survival-rules` (`AGENTS.md:103-111`). `attach --resume` берёт сохранённый sessionID из реестра и открывает его (`cmd/beacon/attach.go:225-261`). Проверки, что состояние сессии не изменилось между выбором id и открытием, в этом пути нет. Подписи для самого журнала нет.

## Карточки

- `docs/research/CC-01.md` флаг параллельности и admission lock.
- `docs/research/CC-02.md` atomic bind владельца worktree.
- `docs/research/CC-03.md` declarative policy и дробные одобрения.
- `docs/research/CC-04.md` delta при отказе patch.
- `docs/research/CC-05.md` revision fence при resume.

Карточки лежат в `docs/research`, а не в `docs/tasks`. Новый ID в `docs/tasks` меняет сгенерированный индекс. Пересобирать индекс в этом docs-PR не предполагалось.
