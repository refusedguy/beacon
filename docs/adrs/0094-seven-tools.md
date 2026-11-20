# ADR 0094. Семь инструментов ядра

Статус: accepted

## Контекст

ADR 0003 выбирает официальный `github.com/modelcontextprotocol/go-sdk` и выводит схемы инструментов из Go-типов. ADR 0009 задаёт единую форму ошибки на границе MCP. Вместе они фиксируют контракт ровно из семи имён из WORKER-PROMPT §4.1: `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_list_running`, `beacon_ack`, `beacon_cancel_watch`, `beacon_stats`.

## Решение

Сохранить ровно семь инструментов. Новые возможности добавляются как расширение существующих инструментов, а не как восьмое имя.

`beacon_watch_pr` расширяется параметрами PR и подписчика: `repo`, `pr`, `headSHA`, `role`, `notify` с адаптером и целью. Он регистрирует или обновляет Watch по ключу идемпотентности и возвращает `watchID`, `revision`, `status`, `verdict`, `pollAfterMs`. Дескриптор `executor` должен быть известен до фразы `waiting, watchID` (ADR 0088). Инструмент аддитивный: он пишет состояние Watch, но не меняет проверки и не мержит PR. Он не помечается read-only.

Реестр читается через два существующих инструмента без записи состояния:

- `beacon_list_running` возвращает снимок активных Watch, счётчики `watches`, `byVerdict`, `subscribers` и курсор с событиями `WatchEvent`. Terminal-статус отдаёт вердикт без проигрывания старых событий (ADR 0025).
- `beacon_stats` возвращает `pointsThisHour`, `budget`, `activeWatches`, блоки `persist`, `loadErrors`, `ladder` и `version`.

Оба инструмента объявлены `ReadOnlyHint: true` и не пишут `readAt` или `claimedAt`. `beacon_wait_result`, `beacon_get_result` и `beacon_ack` остаются аддитивными, `beacon_cancel_watch` остаётся единственным деструктивным инструментом. Порядок имён в `tools/list` частью контракта не является.

### Порядок вывода (HD-045)

Финальный отчёт инструмента строится по правилу Harbor (human-readable summary first, fence после, ADR 0301): сначала идёт человекочитаемое резюме, затем ровно один fence с машинночитаемым JSON. Человек читает резюме без разбора JSON, а оркестратор берёт результат из последнего корректного fence. До открывающего блока JSON в fence должен быть непустой текст. Шаблон, который печатает JSON первым, контракт нарушает.

Шаблон вывода:

````text
Gate verify: 4 checks passed, 0 failed. Watch w-1 is watching.
Machine-readable result follows.

```json
{"schema_version":1,"gate":"verify","status":"pass","blocking":false}
```
````

## Проверка

`tools/list` возвращает ровно семь имён: контрактный тест сравнивает отсортированный список с эталоном и проверяет, что сериализованный ответ укладывается в бюджет 64 KiB. Схемы всех семи инструментов выводятся из Go-типов, а не пишутся вручную. Порядок вывода закреплён тестом `TestToolOutputSummaryBeforeJSON` в `internal/infra/mcp/contract_test.go`: шаблон с непустым резюме до fence проходит, а negative control с JSON первым проваливает тест.

## Приёмка

Восьмой инструмент требует отдельного решения или ADR. Расширение `watch_pr` и чтение реестра через `list_running` и `stats` не увеличивают число имён. Шаблон вывода, который печатает JSON раньше человекочитаемого резюме, проваливает карточку.
