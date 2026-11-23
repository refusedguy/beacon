# VCI-01. Wake с заданием доходит при OPEN breaker и после рестарта Bridge

Premise: проверено 2026-11-23 по свежему `origin/main` `79dc16941bdbb5ff312f85e8602dda0e67be1a2a`. Реализация из PR 1768, 1769 и 1770 уже влита. Для всех трёх merge-коммитов `git merge-base --is-ancestor <SHA> origin/main` вернул 0. Код и тесты на place, дубль писать нельзя. Карточка закрывается с доказательством, без правки OwnerFiles.

Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/cibridge/wake_dispatcher.go, internal/infra/cibridge/wake_deferred_test.go, internal/infra/observe/observe.go, internal/infra/observe/metrics.go
Зависимости: RS-WK-01, RS-WK-02, Q-B-01, ATX-34, FTE-13, CR-01
Ссылка: https://github.com/refusedguy/beacon/pull/1768, https://github.com/refusedguy/beacon/pull/1769, https://github.com/refusedguy/beacon/pull/1770; бэклог `/tmp/opencode/backlog/04-verdict-ci.md`
Агент: vci01-executor
Время: 2026-11-23T06:28:53Z

Цель: Доставить wake с заданием исполнителю независимо от состояния breaker адаптера и сохранить отложенные wake при рестарте Bridge. Метрика считает реальную доставку, а не попытку.

Контекст: До починки actionable wake при OPEN breaker молча терялся, а маркер в таблице obligations никогда не читался назад. Рестарт Bridge стирал припаркованные wake. Каждый defer увеличивал долговый счётчик ошибок и снова открывал breaker. Дополнительно `posted` учитывался после каждого nil-возврата, поэтому схлопнутый дедупликацией wake выглядел как свежая доставка.

Шаги: Сверить premise по свежему origin/main и трём merge-коммитам. Не писать дубль кода в OwnerFiles. Зафиксировать доказательство в новой карточке со статусом done. Открыть PR только с карточкой и зарегистрировать Watch.

Проверка: gofmt не применялся, ни один `.go`-файл не менялся. Локальные `go test`, `go build` и гейты не запускались по правилам AGENTS.md. Тесты уже выполняются в CI на `origin/main`.

Приёмка: Потерь нет, дублей нет, gofmt чистый. Все критерии подтверждены кодом и тестами в main, указанными ниже.

Причина: already_landed
Закрыл: vci01-executor
Время закрытия: 2026-11-23T06:28:53Z

## Подтверждение

Три merge-коммита в `origin/main`, проверка предка вернула 0 для каждого:

- PR #1768, merge SHA `28f65ef0222c8d04dd75a4330b5dba790dbd0571`, `git merge-base --is-ancestor` exit 0.
- PR #1769, merge SHA `da2de52110a8d8b07af45030b80349115afdc294`, `git merge-base --is-ancestor` exit 0.
- PR #1770, merge SHA `3b8de62aa5b3f20df96ee2ff49e87d0b38075bf9`, `git merge-base --is-ancestor` exit 0.

Код на HEAD:

- `internal/infra/cibridge/wake_dispatcher.go:364` `actionableWakeVerdict` пропускает `keep_working`, `rebase`, `retry`, `blocked`, `timeout` мимо breaker. Информационные и терминальные wake остаются под защитой breaker.
- `internal/infra/cibridge/bridge.go:428` префикс `deferred-wake-v1:`, `:430` `encodeDeferredWake` пишет полный JSON с текстом, Watch, Subscriber, адаптером и `cycleID`.
- `internal/infra/cibridge/bridge.go:501` `deferWake` паркует wake и пишет маркер, `:538` `restoreDeferred` читает pending-маркеры при старте, вызов из `New` на `:926`.
- `internal/infra/cibridge/wake_dispatcher.go:535` `replayDeferred` проигрывает отложенные wake, `:562` suppressed по уже закрытому obligationID, `:576` закрывает маркер после доставки.
- `internal/infra/observe/metrics.go:84` `wake_deferred_total`, `:433` гистограмма `wake_deferred_age_seconds`; `internal/infra/observe/observe.go:566` `WakeOutcomeDeferred`, `:753` `WakeDeferred` инкрементирует счётчик один раз на parked wake.

Тесты на HEAD:

- `TestActionableWakeDeliveredWithOpenBreaker` в `internal/infra/cibridge/deferred_replay_test.go:33`: actionable wake уходит при открытом breaker.
- `TestDeferredWakeSurvivesRestartAndReplaysOnRefresh` там же на `:95`: после рестарта отложенный wake доставлен один раз.
- `TestDeferredReplayDoesNotDuplicate` там же на `:117`: повторный Refresh и дубль в очереди не дают второй доставки, маркер закрыт.
- `TestOpenBreakerDefersWakeAndKeepsFailureCounter` и `TestBreakerFailuresSurviveRestart` в `internal/infra/cibridge/wake_deferred_test.go:14` и `:37`.
