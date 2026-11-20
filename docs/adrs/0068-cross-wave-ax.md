# ADR 0068. Пересечение AX: Trail-Code и omnigraph

Статус: accepted

Этот ADR сравнивает два репозитория волны AX: `trailhq/Trail-Code` и `ModernRelay/omnigraph`. Каноническое имя второго репозитория пишется с заглавной `M`, вариант `modernrelay/omnigraph` API резолвит в него. Данные собраны 20 ноября 2026 года через `gh api` и `webfetch`, оба репозитория доступны и не архивированы. Это парное сравнение, а не волна из шести фич: у каждого проекта свой ответ на вопросы, где живёт состояние агента и кто проверяет изменения.

| | Trail-Code | omnigraph |
| --- | --- | --- |
| Описание | общая память кодовых агентов: карта кода и решения команды | графовая БД на объектном хранилище с ветками и типизированной онтологией |
| Язык, лицензия | TypeScript, MIT | Rust, MIT |
| Звёзд, форков | 11348, 1023 | 3107, 340 |
| Открытых issue | 240 | 203 |
| Последний коммит | 19 ноября 2026, `d8a59c1`, Get overlap warnings ready to go live (#738) | 20 ноября 2026, `0deba88`, docs(release) 0.16.0 |

Источники: [Trail-Code README](https://github.com/trailhq/Trail-Code/blob/main/README.md), [omnigraph README](https://github.com/ModernRelay/omnigraph/blob/main/README.md), [Trail-Code через API](https://api.github.com/repos/trailhq/Trail-Code), [omnigraph через API](https://api.github.com/repos/ModernRelay/omnigraph). Даты коммитов взяты из `repos/{owner}/{repo}/commits?per_page=1`, а не из `pushed_at`.

## Решение

Beacon берёт по одному правилу из каждого пересечения. Правила описаны на уровне контракта, без переноса кода TypeScript или Rust.

1. Где живёт состояние. Trail-Code X хранит решения в обычных markdown-файлах `.trail/learnings/`, коммитит их вместе с кодом и работает без сервера и без эмбеддингов. omnigraph Y держит состояние в типизированном графе на объектном хранилище, даёт ветку на агента или задачу, трёхсторонний мерж и просмотр истории графа. Что взять: оставить ADR и карточки Beacon в git как проверяемые файлы, добавить чтение состояния Watch по ветке и по моменту времени поверх SQLite из ADR 0015. Не заменять SQLite на объектное хранилище.

2. Когда срабатывает проверка. Trail-Code X запускает `trail check` перед PR, сравнивает diff с решениями, называет автора и дату и разрешает отступление, объяснённое в коммите или описании PR. omnigraph Y проводит Cedar-политику через все пути записи, один гейт для HTTP, CLI и SDK, и берёт actor из токена на сервере. Что взять: перенести гейт переходов verdict на границу MCP, чтобы все адаптеры подчинялись одному правилу и actor не приходил от клиента. Оставить из X читаемый отказ с указанием ADR и автора и путь объяснённого отступления для мягких проверок.

3. Как находить параллельную работу. Trail-Code X предупреждает, что два агента меняют одну константу в разных ветках, помечает ветки как pushed and not merged yet и не делает мерж сам. omnigraph Y даёт ветку на агента или задачу, трёхсторонний мерж и `cluster plan` перед `cluster apply`. Что взять: показывать оркестратору пересечение файлов двух открытых PR до запуска CI и делать preview изменения состояния до применения вердикта. Трёхсторонний мерж оставить для мультирепо-задач из ADR 0063, фича 6.

## Причина

1. Trail-Code пишет, что решения живут в git и сервер не нужен: learnings это файлы с полями `touches`, `author`, `date` и `cost`, метка `always follow` не даёт агенту пойти против решения, история записей не переписывается ([Part 2](https://github.com/trailhq/Trail-Code/blob/main/README.md#part-2-learnings), [A learning is a file](https://github.com/trailhq/Trail-Code/blob/main/README.md#a-learning-is-a-file)). omnigraph объявляет типы в `.pg`-схемах и проверяет их на каждой записи, хранит граф в открытом формате Lance и ведёт branch, three-way merge и time travel ([Capabilities](https://github.com/ModernRelay/omnigraph/blob/main/README.md#capabilities)). Beacon уже держит решения в git через ADR 0002 и ADR 0007, но состояние Watch из ADR 0015 не имеет типизированного перехода на границе записи. Пересечение даёт два разных ответа: файлы для решений и проверка типов для состояния. Второе ложится на ADR 0062 с CAS-переходами.

2. В Trail-Code отказ `trail check` выглядит так: изменение `defaultRetryMax` с 4 на 10, имя автора, дата, причина и строка «fix it, or say why in the commit message or PR description» ([One decision, two teammates](https://github.com/trailhq/Trail-Code/blob/main/README.md#one-decision-two-teammates)). omnigraph заявляет engine-wide enforcement: HTTP-сервер, CLI и SDK подчиняются одинаковым правилам, actor резолвится сервером и клиент не может его подставить ([Security & governance](https://github.com/ModernRelay/omnigraph/blob/main/README.md#security--governance)). Beacon проверяет гейты в адаптерах и держит правила в ADR 0005 и ADR 0022. Если actor придёт от клиента, квитанция ADR 0028 и вердикт T3.98 потеряют автора. Единый гейт на границе MCP закрывает это, а текст отказа из Trail-Code сохраняет объяснение для человека.

3. Trail-Code показывает случай: агент правит константу, видит предупреждение, что автор другой ветки поднял её до 10 и изменения конфликтуют, и видит ветку как ещё не влитую ([Your team](https://github.com/trailhq/Trail-Code/blob/main/README.md#your-team)). Последний коммит репозитория `d8a59c1` называется Get overlap warnings ready to go live (#738), то есть функция предупреждений о пересечениях развивается. omnigraph даёт `cluster plan` перед `cluster apply` и трёхсторонний мерж веток ([Deploy](https://github.com/ModernRelay/omnigraph/blob/main/README.md#deploy), [Query and mutate](https://github.com/ModernRelay/omnigraph/blob/main/README.md#query-and-mutate)). Beacon видит пересечение только после прогона CI оркестратором. Раннее предупреждение и preview сокращают число повторных прогонов, не меняя правило мержа из ADR: оркестратор мержит после зелёных проверок и прочитанного `verdict=ready`.

## Последствия

- Из каждого пересечения выходит одна карточка в `docs/tasks/`. Карточки созданы этим ADR, статус `todo`, ссылка `ADR 0068`:
  - T3.172, типизированное состояние Watch с чтением по ветке и по времени. Владельцы: `internal/domain/types.go`, `internal/domain/types_test.go`, `internal/domain/transition.go`, `internal/domain/transition_test.go`, `internal/infra/taskstore/store.go`, `internal/infra/taskstore/store_test.go`.
  - T3.173, единый гейт переходов на границе MCP с серверным актором. Владельцы: `internal/infra/mcp/server.go`, `internal/infra/mcp/server_test.go`, `internal/domain/verdict.go`, `internal/domain/verdict_test.go`.
  - T3.174, раннее предупреждение о пересечении веток и preview перед слиянием. Владельцы: `internal/infra/github/rest.go`, `internal/infra/github/rest_test.go`, `cmd/beacon/watch.go`, `cmd/beacon/watch_test.go`.
- Проверка карточек. Недопустимый переход verdict не сохраняется и состояние читается по ветке и по моменту времени; подмена actor в аргументах инструмента не меняет вердикт, а отказ называет правило и автора; пересечение файлов двух открытых PR видно до CI, а preview не меняет состояние.
- ADR 0001-0067 не переписываются. ADR 0015 SQLite остаётся хранилищем, ADR 0019 и ADR 0021 сохраняют жизненный цикл Watch и изоляцию worktree. Единый гейт T3.173 не отменяет ADR 0022 и гейт T3.8. Preview T3.174 не заменяет решение оркестратора о мерже.
- Карта graft из Trail-Code и граф из omnigraph не импортируются как зависимости. Beacon пишет на Go, а графы написаны на TypeScript и Rust. Переносится контракт, а не код.
- Тесты этого ADR не запускались, код не менялся. В PR на реализацию каждой карточки агент приводит команду прогона и фактический вывод.

## Отклонённое

Не брать продукты целиком. Trail-Code требует Node 22.3, parser tree-sitter и hooks в CLI агентов. omnigraph требует Rust stable 2024, protobuf-compiler и для части тестов S3-совместимый endpoint. Оба не подходят как зависимость ядра Beacon. omnigraph Cloud находится в private preview, и зависимость от чужого сервера не годится для self-hosted модели ADR 0029.

Не брать числа из README как показатели Beacon. Trail-Code заявляет 46% меньше вызовов инструментов, 60% меньше времени, рост SWE-bench Verified с 27 до 33 из 50 и 32% меньше стоимости. Это самоотчёт автора, на инфраструктуре Beacon он не воспроизводил. В README omnigraph нет сопоставимых замеров. В контракт переносятся правила, а не числа.

Не брать позицию «вообще без сервера» у Trail-Code и «тонкие stateless-агенты» у omnigraph как схему Beacon. Watch, вердикт и опрос PR требуют сервера по ADR 0003. Из Trail-Code берётся соглашение о файлах решений, из omnigraph проверка типов при записи и ветки с историей.
