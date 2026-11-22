# Сравнение beacon с OpenRig по реальному коду

Дата: 2026-11-22. Метод: поиск репозитория через GitHub API, клонирование с глубиной 1, чтение исходников. Код beacon не менялся. `go test`, `go build`, гейты и `batch-apply` не запускались. Ссылки на beacon даны по `origin/main` на коммите `2d34645b54f45d84dba3a595350f17f8658759da`.

## 1. Источник

| Поле | Значение |
| --- | --- |
| Репозиторий | `mvschwarz/openrig`, <https://github.com/mvschwarz/openrig> |
| Каталог клона | `/tmp/opencode/openrig-research` |
| Коммит | `6793401380e3685042ffbe8caa686cb9771bf7ca` от 2026-11-22 |
| Версия пакета | `0.7.1` (`package.json`) |
| Поиск | `gh api "search/repositories?q=openrig"`, 147 результатов; выбран репозиторий с описанием "persistent teams with roles, shared context and owned work" |

OpenRig держит состояние в SQLite, запускает агентов в tmux как обычные сессии Claude Code, Codex, Pi или OMP и добавляет вокруг них координацию: запуск и восстановление, доставку сообщений, очередь, контекст. Прочитаны `ARCHITECTURE.md`, `docs/reference/worktree-builds.md`, `docs/reference/instance-layout.md`, `skills/_canonical/core/openrig-software-factory/SKILL.md` и перечисленные ниже исходники.

| Файл OpenRig | Что прочитано |
| --- | --- |
| `packages/daemon/src/domain/topology/multi-rig-launcher.ts` | Ограниченный параллельный запуск ригов, стоп после провала, явные `skipped` |
| `packages/daemon/src/domain/seat-delivery-guard.ts` | Лизинги по отсортированным ID, проверка цели после ожидания, привязка к сессии |
| `packages/daemon/src/domain/node-launcher.ts` | Резервирование адреса и поколения владельца, транзакционная регистрация |
| `packages/daemon/src/domain/startup-orchestrator.ts` | Отложенный контекст запуска и отмена продолжения по свежему событию |
| `packages/daemon/src/domain/crash-cart-conductor.ts` | Восстановление флота по порядку, best-effort, `not_attempted` с причиной |
| `packages/daemon/src/domain/scope/scope-approve.ts` | Подпись интеграции: печать, аудит, откат байтов, аргументированный переподпис |
| `packages/daemon/src/domain/cwd-resolution.ts` | Отказ запускать агента внутри каталога установки оркестратора |
| `packages/daemon/src/domain/review/fleet-compose.ts` | Общий бюджет для параллельных чтений хостов |
| `docs/reference/worktree-builds.md` | Самопроверка, что сборка в worktree использует исходники этого же дерева |

## 2. Найденные приёмы и разрывы

Состояние beacon проверено точечно через `git show origin/main:<path>` и `git grep` по `origin/main`.

| Идея | Источник, file:line | Разрыв в beacon, file:line | Измеримая проверка | Карточка |
| --- | --- | --- | --- | --- |
| Параллельный запуск с фиксированным капом, стопом после провала и явным `skipped` | `packages/daemon/src/domain/topology/multi-rig-launcher.ts:78-82`, `:112-173`; `packages/daemon/src/domain/review/fleet-compose.ts:49-53`, `:359-370` | `internal/infra/cibridge/wake_dispatcher.go:122-131` прямо говорит, что окно отказов не ограничивает параллелизм; `internal/app/tasks/night_shift.go:191-193`, `:208-214` последовательный цикл с обрывом на первой ошибке; `internal/app/orchestrator/workload.go:142-187` только считает Watch | При 12 заданиях и капе 4 максимум одновременно в работе не выше 4; после провала на третьем задании девять последующих имеют явный статус `skipped`, а не отсутствуют | CR-01 |
| Сортированный лизинг перед мутацией и самопроверка владения worktree | `packages/daemon/src/domain/seat-delivery-guard.ts:422-441`, `:469-474`; `packages/daemon/src/domain/node-launcher.ts:216`, `:227`, `:351-385`; `docs/reference/worktree-builds.md:44-45` | `internal/infra/worktree/gc.go:176-220` проверяет только locked, защищённый каталог, точный HEAD и `git status`; правила в `internal/infra/worktree/headguard.go:45-103` есть и покрыты тестом `:37`, но `headguard.go:3-4` прямо говорит, что сборщик их не вызывает; `internal/infra/worktree/grace.go:50-73` делает вторую проверку статуса, но не держит лизинг | При параллельных collect и handoff одного worktree collect не удаляет каталог, пока держится лизинг; worktree с HEAD потомком Watch остаётся с причиной `head_ancestor` | CR-02 |
| Продолжение только если контекст ещё актуален; неопределённая доставка не повторяется | `packages/daemon/src/domain/startup-orchestrator.ts:65-78`, `:103-117`, `:1111-1116` | `internal/app/taskcli/service.go:2522-2543` отдаёт все шаги журнала и `LastSeq`, но не сверяет их с самым свежим событием; `:2803-2806` сохраняет `inconclusive` как ответ; дедупликация в `internal/infra/cibridge/wake_dispatcher.go:42-69` привязана к обязательству, а не к последнему событию | После шагов `a`, `b` и более свежего события `continued` проверка продолжения возвращает false и не предлагает `a`, `b` повторно; некорректный seq означает отказ в продолжении | CR-03 |
| Восстановление по единицам с закрытым набором исходов, причиной и действием | `packages/daemon/src/domain/crash-cart-conductor.ts:14`, `:49-90`, `:161-220`, `:277-299`, `:352-371` | `internal/app/tasks/night_shift.go:191-193`, `:210-214` обрывает проход при первой ошибке; `internal/app/orchestrator/daemon.go:83-84`, `:111-126` логирует и глотает ошибки шагов без исхода по каждой единице | Три кандидата, средний проваливается: результат содержит три строки с исходами `delegated`, `failed` с причиной и `not_attempted` с действием; сейчас `Run` возвращает ошибку после первой и выдаёт только одного кандидата | CR-04 |
| Аргументированное повторное подтверждение с историей и откатом байтов | `packages/daemon/src/domain/scope/scope-approve.ts:47-55`, `:239-268`, `:310-370` | `internal/infra/mcp/tool_ack.go:110-141` не имеет поля причины; `:194-212` хранит одно решение на Watch; `:250-268` отказ от второго решения; `:677-706` закрепляет первый ответ. В `tool_task_close.go:41-47`, `:249-252` причина и идемпотентность уже есть, но это другой путь | Повторный approve с новой причиной создаёт вторую запись с `prior_by`, `prior_at`, `prior_count` и сохраняет первую; текущая реализация отвергает вызов с `CodeInvalidInput` | CR-05 |

## 3. Что уже есть в beacon и не требует карточек

| Проверенная идея | Где лежит | Почему карточку не создаём |
| --- | --- | --- |
| Атомарный claim карточки | `internal/infra/mcp/tool_task_claim.go:88-94`, `:169-177` | Flock на общий файл карточек, выбор и запись в одном критическом участке. Два агента не получают одну карточку. |
| Защита worktree по active Watch и состоянию | `internal/infra/worktree/gc.go:38-55`, `:89-120`, `:122-165`; `internal/infra/worktree/marker.go:18-25`, `:106-117` | Main worktree не кандидат, грязные и заблокированные каталоги остаются, маркер агента защищает каталог в течение TTL. Разрыв CR-02 только в наборе правил защиты, а не в самой возможности удалять безопасно. |
| Дедупликация wake по обязательству | `internal/infra/cibridge/wake_dispatcher.go:42-69`; `internal/infra/cibridge/verdict_applier.go:843-844` | Доставленный obligation подавляет повтор. CR-03 про другую ось: актуальность шагов в журнале передачи, а не повтор одной доставки. |
| Мерж-гейт с ревизией и подтверждением состояния | `internal/app/orchestrator/orchestrator.go:65-93`, `:141-182`, `:217-235`, `:249-266` | Мерж разрешён только по `ready`, зависимости проверяются резолвером, а закрытие Watch конфликтует по ревизии и headSHA. Это сильнее аналога в OpenRig. |
| Журнал передачи с жёсткими лимитами | `internal/app/taskcli/service.go:2440-2469`, `:2790-2833` | Журнал ограничен 16 записями и 64 КБ, подтверждение continued фиксируется фактом. CR-03 добавляет проверку свежести, а не новый журнал. |
| Проверка путей в worktree | `docs/research/research-analogs-beads-dmux.md` (AN-06) | Идея хуков жизненного цикла worktree уже описана в другой карточке. CR-02 описывает лизинг и правила защиты, а не хуки. |

## 4. Приоритеты

1. CR-02. Готовый и протестированный `GuardWatch` не подключён к сборщику. Это самый дешёвый разрыв с самой высокой ценой ошибки.
2. CR-01. Нет настраиваемого ограничения параллелизма и явных пропущенных единиц в night shift. Без капа число агентов растёт вместе с бэклогом.
3. CR-04. Обрыв прохода на первой ошибке оставляет остальные задачи без диагноза, что мешает ночной смене.

CR-03 и CR-05 требуют изменения контракта, поэтому идут после трёх первых.

## 5. Ограничения чтения

OpenRig не создаёт git-worktree на агента в прочитанных исходниках. Его изоляция это отдельный cwd сессии, tmux-сессия, лизинг доставки и владельческое поколение. Проверка `node_modules` в `docs/reference/worktree-builds.md` относится к сборке OpenRig в worktree, а не к изоляции агентов. В CR-02 используется только принцип проверки владения, а не утверждение, что OpenRig создаёт git worktree на задачу.

Документация OpenRig может отставать от кода. `ARCHITECTURE.md` прямо помечен как потенциально устаревший, поэтому все формулировки выше проверены по исходникам на коммите `6793401`.
