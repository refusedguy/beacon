# E11_metrics_counters

## Сценарий

Поднять макет: тестовый OTLP-коллектор с приёмом `/v1/traces` и процесс `beacon mcp` с `BEACON_OTEL_ENABLED=true`, `BEACON_OTEL_EXPORTER=otlp`, `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<порт>` и `OTEL_EXPORTER_OTLP_PROTOCOL=http/json`. Метрики поднять на `BEACON_METRICS_PORT`.

Создать 15 активных вотчей. Снять `/metrics`. Отправить один спан и не вызывать `Shutdown`. Подождать дольше 5 с, интервала partial flush из OBS-01. Проверить, что коллектор получил спан.

Удержать блокировку поллера и снять `/metrics` повторно. Записать значения `beacon_poller_active` и `beacon_poller_pid`.

Отдельно запустить `beacon doctor` с неверным `BEACON_OTEL_EXPORTER` и записать вывод.

## Проверки

- счётчики `/metrics` совпадают с `beacon_stats`
- RSS не выше цели 20-30 МБ. Цель остаётся целью, фактическое значение записывается в отчёте eval
- процессы `gh` не растут за время сценария
- коллектор получил спан, отправленный без `Shutdown`
- `/metrics` содержит `beacon_poller_active 1` и `beacon_poller_pid` больше нуля, пока поллер держит блокировку
- `beacon doctor` печатает WARN о неверном `BEACON_OTEL_EXPORTER` после влития OBS-07

## Негативный контроль

- `watchID`, `pr` и `sessionID` не появляются в именах и labels `/metrics`. Метрики с такими labels дают взрыв серий
- если коллектор не получил спан, eval помечает потерю трассировки отдельно от состояния поллер-gauges
- если `beacon_poller_active` и `beacon_poller_pid` равны нулю при активном поллере, eval помечает отсутствие poller-gauges отдельно от трассировки
- если `beacon doctor` не печатает WARN о неверном `BEACON_OTEL_EXPORTER`, eval помечает пропущенную проверку конфигурации после влития OBS-07

## Отчёт

Каждая проверка записывает имя, ожидаемое значение, фактическое значение и статус PASS или FAIL в отчёте eval.
