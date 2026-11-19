# ADR 0033. Три идеи из отчёта R-graft для ночной смены

Статус: accepted

Этот ADR фиксирует три идеи из отчёта R «graft, opengraft и мультитуллы для Beacon» от 19 ноября 2026 года, раздел «Фичи для Beacon» (пункты 1, 3 и 5). Владелец велел принять их без дополнительных вопросов, поэтому статус `accepted`. По ADR 0020 карточки создаются только из принятого ADR. Ни одна из трёх идей не меняет ADR 0024-0032 и не ломает ограничения Beacon: один статический Go-бинарник, stdlib где возможно, SQLite без cgo, ровно 7 MCP-инструментов, проверка только в CI. Карточки этим ADR не создаются.

## 1. Дедупликация GraphQL-батча по head SHA

Решение. Внутри одного GraphQL-батча каждый head SHA опрашивается один раз. Повторные PR с тем же SHA в разных тирах получают результат из уже выполненного запроса, а не запускают новый.

Причина. Отчёт R, пункт 1 [R-graft, строка 185](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L185): [kudzu](https://github.com/garyships/kudzu/blob/main/README.md) превращает 50 одинаковых холодных запросов к одним объектам в один запрос наверх, то есть coalescing на уровне кэша. В Beacon уже есть батч GraphQL на 15 PR за один поинт, источник `RESEARCH_SUMMARY.md`. Дедупликация по head SHA это следующий шаг внутри уже принятой архитектуры. Кэш по head SHA зафиксирован решением 7 в `DECISIONS_FROM_DIALOGUE.md`, а неизменяемость объекта позволяет переиспользовать ответ без проверки у сервера ([Scion](https://www.anthropic.com/engineering/redesigning-source-control); [R-graft, строка 209](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L209)). Экономия измеряется в поинтах GitHub: 15 процессов `gh pr checks --watch` это 5400 запросов в час при лимите 5000, против одного поинта за батч ([R-graft, строка 207](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L207)).

Последствия. Появляется ключ дедупликации по head SHA в рамках одного батча и один запрос на уникальный SHA вместо одного запроса на PR. Счётчик поинтов из ADR 0013 не меняется: батч остаётся одним поинтом. Владельцы файлов для будущей карточки: `internal/infra/github/graphql.go`, `internal/infra/github/tier1.go`, `internal/infra/github/tier2.go`, `internal/infra/github/cache.go` и тесты к ним.

Отклонённое. Опрос каждого PR отдельно с тем же head SHA. Тратит поинты и запросы на данные, которые уже получены в этом же батче. Также отклоняется общий распределённый кэш: в одном процессе дедупликация делается проще ([R-graft, строка 211](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L211)).

## 2. Конфликт как структурированный JSON-ответ вместо текста ошибки

Решение. Конфликт возвращается вызвавшему агенту как структурированный результат с типом конфликта и путями конфликтующих файлов, а не как текст ошибки. Приоритет задач и намерение пробуждения фиксируются до ответа по уже принятой логике defer wake из ADR 0017 и 0026.

Причина. Отчёт R, пункт 3 [R-graft, строка 189](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L189): [elffuss-braid](https://github.com/utopia-ia/elffuss-braid/blob/main/README.md) возвращает конфликт в JSON с кодом выхода 2 и не двигает main; агент разбирает такой ответ без чтения текста ошибки. Для MCP-ответа Beacon это хорошо ложится на случай, когда два вызвавших агента претендуют на одну задачу. Отчёт отдельно отмечает эту же модель у [jj-vcs/jj](https://github.com/jj-vcs/jj): зафиксировать намерение сразу, разрешить конфликт позже ([R-graft, строка 124](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L124)).

Последствия. В протокол ответа добавляется тип конфликта и список путей файлов; текст ошибки перестаёт быть единственным источником деталей. Число MCP-инструментов остаётся равным семи, ADR 0003 и 0009 не меняются. Владельцы файлов для будущей карточки: `internal/domain/types.go`, `internal/domain/verdict.go`, `internal/infra/mcp/schema.go`, `internal/infra/github/errors.go` и тесты к ним.

Отклонённое. Возвращать конфликт как строку сообщения об ошибке. Агент вынужден разбирать текст, а тип конфликта и пути файлов теряются. Отклоняется и импорт самого braid как зависимости: Rust, свой формат данных, нет MCP ([R-graft, строка 93](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L93)).

## 3. Дешёвая песочница через `git clone --reference` вместо Rust/FUSE

Решение. CLI-менеджер создаёт изолированную рабочую копию через `git clone --reference`: общий object store, изолированные refs, index, HEAD и рабочее дерево. Виртуальная копия не копирует данные репозитория заново. git остаётся источником истины по ADR 0021.

Причина. Отчёт R, пункт 5 [R-graft, строка 193](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L193): [penacristian/clone-per-agent](https://github.com/penacristian/clone-per-agent/blob/main/README.md) на репозитории с 80 MB истории даёт `.git` по 688 KB на клон и время клона 0 секунд. Это самый простой вариант, который можно попробовать без Rust и без FUSE. Цель по времени создания песочницы задана [404khai/graft](https://github.com/404khai/graft/blob/main/README.md): виртуальный graft за 18-19 мс и около 1.1 MiB на 200 000 файлов ([R-graft, строка 191](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L191)). Та же идея измерения конкретной операции, а не лозунга «новое быстрее», зафиксирована в [R-graft, строка 68](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L68).

Последствия. Появляется способ provisioning песочницы для агентов без копирования object store на каждый worktree; изоляция refs и index сохраняется. ADR 0021 и 0029 уточняются в части механизма, но не отменяются: менеджер по-прежнему не правит код в worktree агента. Владельцы файлов для будущей карточки: `cmd/beacon/agents.go`, `cmd/beacon/attach.go`, `internal/app/taskcli/service.go` и тесты к ним.

Отклонённое. Брать сам 404khai/graft или любой graft как зависимость: Rust, два бинарника `graft` и `graftd`, FUSE или reflink на XFS с привилегиями, не влезает в один Go-бинарник ([R-graft, строка 13](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L13)). Отклоняется FUSE и reflink как обязательное требование ([R-graft, строка 239](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L239)), а также замена git как таковая ([R-graft, строка 231](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L231)).

## Отклонённое без привязки к идее

Arena allocator не берётся ни при какой реализации трёх идей. Пакет `arena` в Go 1.26.7 спрятан за `//go:build goexperiment.arenas` ([исходник](https://github.com/golang/go/blob/go1.26.7/src/arena/arena.go)); `GOEXPERIMENT` меняет служебный блок сведений о сборке и ломает побайтовое совпадение закоммиченного бинарника гейтов с пересборкой в CI по ADR 0023 ([R-graft, строка 215](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L215), отчёт `K-go-version-ci.md`). Вместо арены используются `sync.Pool` и `sync.Map` из stdlib ([R-graft, строки 217-219](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L217)). Rust и C как язык новых компонентов не принимаются, как и лицензии GPL ([R-graft, строки 233-235](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L233)). Три процесса server/cache/daemon не вводятся: Beacon остаётся одним процессом ([R-graft, строка 241](/home/nbook/Projects/tmp/beacon-research/R-graft-and-multi_tool.md#L241)).
