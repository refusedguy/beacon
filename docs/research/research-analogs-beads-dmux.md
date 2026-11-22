# Аналоги beads, dmux и Taskwarrior для бэклога Beacon

Дата: 2026-11-22. Метод: прямое чтение официальных репозиториев и man-страниц через `webfetch`. Websearch вернул HTTP 402, поэтому поиск заменён прямыми URL. Код не менялся, `go test`, `go build`, гейты и `batch-apply` локально не запускались. Ссылки на код даны по `origin/main` на коммите `f25294ba`.

## 1. Первичные источники

| Источник | Что прочитано | Дата |
| --- | --- | --- |
| Beads, README | Hash-ID `bd-a1b2`, `bd ready`, `bd update --claim`, `bd close`, Dolt sync, JSON output, `bd prime`, compacting, защита от коллизий при слиянии веток | 2026-11-22 |
| Beads, Dependencies and Gates | `bd dep add`, четыре блокирующих и шесть неблокирующих типов рёбер, цикл отвергается на записи, `bd dep cycles`, `bd blocked`, gates `gh:pr`/`gh:run`/`timer`/`bead`/`human`, `bd gate check` | 2026-11-22 |
| dmux, README | Один tmux pane на задачу, отдельный worktree и ветка на pane, lifecycle hooks на create и merge, smart merging, возобновление диалога, явное имя ветки по билету | 2026-11-22 |
| Taskwarrior, man page task(1) | `task ready`, `task blocked`, `task blocking`, `task unblocked`, виртуальные теги `READY`/`BLOCKED`/`BLOCKING`/`UNBLOCKED`, `depends:`, `task next` по urgency, `export`/`import` JSON, `task sync`, стабильный UUID | 2026-11-22 |

Репозиторий beads за время жизни переехал из `steveyegge/beads` в `gastownhall/beads`. Страницы, которые я читал, отдаются по старому URL и содержат ссылки на новый. API от этого не зависит, но в карточках лучше указывать оба имени.

## 2. Таблица идей

Состояние указано по `origin/main` `f25294ba`. Частично означает, что примитив в коде есть, а пользовательского сценария нет.

| Идея | Аналог и источник | Состояние в beacon | Разрыв | Приоритет |
| --- | --- | --- | --- | --- |
| AN-01. Ready-очередь как запрос: перечислить задачи без открытых блокировщиков | `bd ready`, `bd blocked` (beads dependencies doc); `task ready`, `task blocked`, `task unblocked`, тег `READY` (Taskwarrior man page) | Частично. Граф из четырёх рёбер в `internal/infra/taskstore/edges.go:67`, `:96`, `:123`. Цепочка блокировок доступна через `beacon task why` в `cmd/beacon/task.go:242`. | Нет команды и MCP-поля, которые выдают список готовых задач. `beacon task` содержит `list`, `new`, `take`, `why`, но не `ready` и не `blocked`, см. `cmd/beacon/task.go:53-71`. Выборка на стороне claim это AUTOSP-05, сюда не входит. | P1 |
| AN-02. Каскад разблокировки после закрытия карточки | Beads: закрытие issue снимает блокировку, и задача появляется в `bd ready` (beads README, dependencies doc) | Частично. `internal/infra/store/card_links.go` хранит связь, `internal/infra/cibridge/bridge.go:178-185`, `:678` закрывает карточку при `merged`. Ссылка на PR пишется по `AGENTS.md:62`. | Никто не пересчитывает зависимые карточки после закрытия. Ни один порт в `cibridge` не публикует событие разблокировки. Ready-запрос из AN-01 поэтому не получает обновления. | P1 |
| AN-03. Внешние гейты как узлы графа: PR, CI, таймер, человек | Beads gates `gh:pr`, `gh:run`, `timer`, `bead`, `human` и `bd gate check`, эскалация сорванного гейта (beads dependencies doc) | Частично. Watch имеет вердикты, `waitFor`, `waitForMain` и авто-мерж, `README.md:189`. Решение владельца приходит через `beacon_ack` с `blocked:not_mine`. | Гейт живёт только в состоянии Watch. Карточка не может объявить блокировку до PR номер N в зависимостях. `beacon task why` читает только рёбра карточек и не видит внешнее условие. | P1 |
| AN-04. Хеш-ID вместо последовательного счётчика | `bd-a1b2`, хеш-ID защищают от коллизий при слиянии веток (beads README) | Отсутствует. `internal/infra/taskstore/store.go:547` считает `T%d.%d` как максимум счётчика плюс один. `store.go:509` хранит счётчик в JSON. `store.go:391` считает `Hash8`, но только для имени файла лока и счётчика. | Две ветки создают `T3.99` независимо. `CreateExclusive` в `store.go:560` ловит конфликт только внутри одного дерева. Межветвевой коллизии нет ни детекта, ни автоматического разрешения. | P2 |
| AN-05. JSON round-trip для задач и графа | `task export`/`task import` JSON, `task _get`, `task _ids`, стабильный UUID (Taskwarrior man page); `bd ready --json`, `bd graph` (beads README) | Частично. `beacon task list --format json` работает через `internal/app/taskcli/service.go:225`. Пакетный JSON есть в `cmd/batch-apply`. | Нет `beacon task export`/`import` и нет JSON для `why` и графа. `runTaskWhy` в `cmd/beacon/task.go:242` печатает только текст. Перенос бэклога между машинами идёт через git, а не через экспорт. | P2 |
| AN-06. Хуки жизненного цикла worktree | dmux: хуки на creation, pre-merge, post-merge; одна задача на worktree; smart merging (dmux README) | Частично. Worktree на агента описаны в `AGENTS.md:9`, `:69`. Авто-мерж по `ready` включён по умолчанию, `README.md:189`. Возобновление после смерти в `AGENTS.md:105-111`. | Нет объявленных точек расширения на переходе claim, merge и close. В репозитории только `pre-commit` и `pre-push` в `.githooks/`. Скрипт проекта не может подписаться на взятие или закрытие карточки. | P2 |

## 3. Уже есть, новых карточек не нужно

| Проверенная идея | Где лежит | Почему не создаём карточку |
| --- | --- | --- |
| Атомарный claim | `internal/infra/mcp/tool_task_claim.go:171`, `:200`, `:238`; `internal/app/taskcli/service.go:676` | Flock берётся до чтения, чтение повторяется под локом, статус проверяется перед записью. Два агента не получат одну карточку. |
| Циклы на записи | `internal/infra/taskstore/edges.go:67`, `:86`, `:96`; тест `edges_test.go:85` | Граф отклоняет петлю и ребро, замыкающее путь. Отдельная команда `bd dep cycles` здесь нужна только как read-side отчёт. |
| Выбор по фазе и зависимостям в claim | `docs/tasks/AUTOSP-05.md` | Карточка уже ставит OrderByPhase, фильтр `dropped` и проверку `depends-on` через порт графа. AN-01 намеренно не трогает claim. |
| Связь карточки с Watch | `docs/tasks/AUTOSP-09.md`, `internal/infra/store/card_links.go` | Explicit cardID и watchID уже описаны отдельной карточкой. |
| Закрытие по мержу | `docs/tasks/IDX-08.md`, `internal/infra/cibridge/bridge.go:178` | Терминальный `merged` уже ставит `done` и ссылку на PR. AN-02 работает ниже по потоку. |
| Инкрементальный индекс | `docs/tasks/BCX-40.md`, статус `done` | `raw_sha256` уже используется для обновления только изменённых карточек. |
| Лизинг и TTL claim | `docs/research/task-claim-patterns-brainstorm.md:85-95` | Идея уже записана с сроком аренды и heartbeat. Дублировать не нужно. |
| Причины закрытия | `docs/research/analog-issue-trackers.md`, `docs/research/analog-github-native.md` | GitHub, Linear, Jira и YouTrack уже разобраны с первичными источниками. |
| Офлайн-работа и синхронизация | Карточки в git как источник истины, индекс выводимый, `AGENTS.md:48-52`; офлайн-режим описан в beads README | Git уже переносит состояние. Отдельный аналог Dolt в Beacon не нужен, пока карточки лежат в дереве git. |

## 4. Топ-5 идей с источниками

1. AN-01, ready-очередь как запрос. `bd ready` и `bd blocked` из beads dependencies doc, <https://raw.githubusercontent.com/steveyegge/beads/main/docs/core-concepts/dependencies.md>. `task ready` и `task blocked` из Taskwarrior man page, <https://raw.githubusercontent.com/GothenburgBitFactory/taskwarrior/develop/doc/man/task.1.in>.
2. AN-02, каскад разблокировки после закрытия. Beads README, <https://raw.githubusercontent.com/steveyegge/beads/main/README.md>.
3. AN-03, внешние гейты в графе. Таблица gate types и `bd gate check` в beads dependencies doc, <https://raw.githubusercontent.com/steveyegge/beads/main/docs/core-concepts/dependencies.md>.
4. AN-04, хеш-ID. Пункт про hash-based IDs в beads README, <https://raw.githubusercontent.com/steveyegge/beads/main/README.md>.
5. AN-05, JSON round-trip. `task export`/`task import` в Taskwarrior man page, <https://raw.githubusercontent.com/GothenburgBitFactory/taskwarrior/develop/doc/man/task.1.in>.

AN-06 с dmux замыкает список, но приоритет P2. Первые три идеи дают прямую выгоду. Автоспавнер из AUTOSP-04 получит проверяемый список готовых карточек вместо первого ID по алфавиту.

## 5. Чего не нашёл

Публичный README dmux не описывает API для графа задач, атомарного claim, JSON-протокола или собственного трекера. dmux оркестрирует worktree и агентов, а не хранит зависимости. Любые утверждения о таком API были бы выдумкой.

Beads не публикует в прочитанных документах отдельную команду анализа влияния. Ближайшее это `bd dep tree --direction=both` и `bd graph --all`. Beacon уже имеет `Traverse` и `TraverseAll` в `edges.go:123` и `:152`, то есть примитив есть. Разрыв только в том, что обход не открыт наружу.

Сайт `taskwarrior.org/docs/ready/` вернул HTTP 503. Я использовал официальную man-страницу из репозитория Taskwarrior. Дата чтения 2026-11-22.

## 6. Список источников

- <https://raw.githubusercontent.com/steveyegge/beads/main/README.md>, прочитано 2026-11-22.
- <https://raw.githubusercontent.com/steveyegge/beads/main/docs/core-concepts/dependencies.md>, прочитано 2026-11-22.
- <https://raw.githubusercontent.com/standardagents/dmux/main/README.md>, прочитано 2026-11-22. Каталог подтверждён через GitHub API, <https://api.github.com/search/repositories?q=dmux+agent+orchestration>, прочитано 2026-11-22.
- <https://raw.githubusercontent.com/GothenburgBitFactory/taskwarrior/develop/doc/man/task.1.in>, прочитано 2026-11-22.
- Локальные карточки и отчёты перечислены в разделах 2 и 3 с путями внутри репозитория.
