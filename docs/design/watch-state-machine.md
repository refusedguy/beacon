# Модель жизненного цикла Watch

PSK-03 добавляет проверяемую модель трёх правил Watch и кратчайший путь до каждого нарушения. Модель лежит в `docs/design/watch-state-machine.tla`, проверку запускает `scripts/verify-watch-model.sh`. Модель не импортируется в Go-код и не меняет `go.mod`.

## Что проверяется

| Инвариант | Правило | Источник в коде |
| --- | --- | --- |
| `NoDoubleMerge` | Один Watch мержится не более одного раза | `closeWatch` в `internal/infra/cibridge/verdict_applier.go`: переход без роста ревизии возвращает 0, `applyMerge` вызывает его после успешного мержа |
| `VerdictNeedsRevision` | Вердикт не меняется без новой ревизии | `transitionVerdict` в `internal/domain/transition.go`: одинаковый вердикт не меняет состояние, при изменении ревизия растёт на единицу |
| `BlockClearedOnlyByResolved` | Блок снимается только решением `blocked:resolved` | `blockWatch` и `unblockWatch` в `internal/domain/transition.go`, `beacon_ack` с `decision="blocked:resolved"` |

## Переменные и соответствие коду

Модель хранит `watch`, `status`, `revision`, `verdict`, `subscriber`, `mergeLedger` и три пары `lastFrom*`/`lastTo*` для проверки изменения вердикта, плюс `blockClearReason`.

`mergeLedger[watch]` считает успешные мержи. Проверка «уже замержено» в модели это `mergeLedger[watch] = 0`. В коде ей соответствует ранняя проверка в `closeWatch`: если `Transition` не поднял ревизию, метод ничего не пишет и возвращает 0.

`revision` растёт на единицу при смене вердикта, включая понижение до `watching` на том же SHA. Переход `watching -> watching` ревизию не трогает. Защита от переполнения из `bumpRevision` в модель не включена: граница `MaxRevision` останавливает поиск раньше.

Действия `EvaluateWatching`, `EvaluateReady`, `EvaluateRebase`, `EvaluateRetry`, `EvaluateKeepWorking`, `EvaluateTimeout`, `EvaluateMerged`, `EvaluateClosed`, `EvaluateBlocked` и `EvaluateRecheck` соответствуют строкам таблицы вердиктов в `internal/domain/verdict.go`. Каждое действие помечено отдельно, чтобы путь TLC показывал конкретную строку. Дополнительные действия `DoMerge`, `ReobserveMerged`, `Block`, `ClearBlock` и `SwitchWatch` покрывают пути из `verdict_applier.go` и `transition.go`, которые таблица вердиктов сама не описывает.

## Параметры и границы

Параметры заданы константами в начале TLA-файла:

- `Watches` из двух идентификаторов. Второй нужен, чтобы проверить, что счётчик мержа хранится на Watch, а не глобально.
- `Subscribers` из трёх ролей (`executor`, `orchestrator`, `fixer`).
- `Verdicts` из десяти значений, которые описаны в `GLOSSARY.md` и `internal/domain/types.go`.
- `Revisions` это `0..MaxRevision`, где `MaxRevision = 6`.

Границы модели:

- Время и таймеры не моделируются. VerdictTimeout это обычное действие, а не результат истечения `DeadlineAt`.
- Ladder, повторные доставки и обязательства подписчиков не моделируются. Модель проверяет жизненный цикл Watch, а не очередь wake.
- Условия принятия решения из `EvaluatePR` не моделируются. Модель начинается с уже выбранного вердикта и проверяет только его применение.
- Политики `requireApproval`, `waitFor` и `waitForMain` не моделируются. Они удерживают мерж до вызова `applyMerge` и не влияют на три проверяемых правила.
- Смена head закрывает Watch и создаёт преемника с нулевой ревизией. История `lastFrom*`/`lastTo*` при этом сбрасывается, так что инвариант вердикта проверяется внутри одного Watch.
- Модель не проверяет `MergeError`, `BaseFailedChecks` и прочие поля `Snapshot`.

Ограниченное число ревизий и Watch делает граф конечным и позволяет TLC пройти его полностью. Это границы проверки, а не ограничения продукта.

## Воспроизведение

Нужны `bash`, `java` 17 или новее и `curl` для первой загрузки инструмента. Скрипт фиксирует TLC версии 1.8.0 и проверяет контрольную сумму `fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713`. Jar кэшируется в `${XDG_CACHE_HOME:-$HOME/.cache}/beacon/tlc`. Если сети нет, путь к локальному jar передаётся через `TLC_JAR`.

```bash
bash scripts/verify-watch-model.sh
```

Скрипт проверяет базовую модель, затем по очереди убирает каждый из трёх guard и ждёт, что TLC упадёт с нужным инвариантом и напечатает путь. После этого он возвращает guard на место и снова проверяет базовую модель. Код возврата 0 означает, что все три нарушения найдены, а модель восстановлена.

Контрольную правку можно запустить отдельно. Скрипт завершится с кодом TLC, то есть ненулевым, и напечатает кратчайший путь:

```bash
bash scripts/verify-watch-model.sh --patch MERGE_ONCE
bash scripts/verify-watch-model.sh --patch VERDICT_REVISION
bash scripts/verify-watch-model.sh --patch BLOCK_RESOLVED_ONLY
```

Guard заданы одной строкой каждый (`MERGE_ONCE`, `VERDICT_REVISION`, `BLOCK_RESOLVED_ONLY`), поэтому снятие проверки это замена `TRUE` на `FALSE`. Скрипт меняет только копию спецификации во временном каталоге. Исходный TLA-файл остаётся неизменным.

## Результаты проверки

Проверка выполнена на TLC версии 1.8.0, workers 1. Базовая модель: 106789 сгенерировано состояний, 9324 различных, глубина полного графа 12. TLC не нашёл ошибок.

| Снятый guard | Инвариант | Путь до нарушения |
| --- | --- | --- |
| `MERGE_ONCE` | `NoDoubleMerge` | 3 состояния: `Init -> Emit("merged") -> ReobserveMerged`. Второй мерж увеличивает `mergeLedger[watch]` до 2 |
| `VERDICT_REVISION` | `VerdictNeedsRevision` | 2 состояния: `Init -> Emit("ready")`. Вердикт сменился с `watching` на `ready` при ревизии 0 |
| `BLOCK_RESOLVED_ONLY` | `BlockClearedOnlyByResolved` | 3 состояния: `Init -> Block -> ClearBlock("watching")`. Блок снят решением `watching`, а не `blocked:resolved` |

Пути получены из вывода TLC в режиме breadth-first, поэтому это кратчайшие контрпримеры для данных параметров. Полный вывод содержит значение каждой переменной в каждом состоянии.

Сверка с кодом: путь для `MERGE_ONCE` повторяет ситуацию, в которой `applyMerge` получает повторный `MERGED` после успешного мержа, а `closeWatch` должен отказать из-за неизменившейся ревизии. Путь для `VERDICT_REVISION` повторяет смену вердикта в `transitionVerdict` без вызова `bumpRevision`. Путь для `BLOCK_RESOLVED_ONLY` повторяет попытку снять блок по обычному вердикту CI, тогда как `transitionVerdict` игнорирует вердикты на заблокированном Watch и снятие проходит только через `unblockWatch`.

Добавление модели не меняет число MCP-инструментов, протокол Watch и required-проверки CI.
