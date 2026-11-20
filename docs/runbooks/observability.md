# Observability: логи и метрики Beacon

Beacon пишет структурированные логи в файл и держит счётчики в памяти процесса. stderr используется только для ошибок настройки логгера. Обычные записи, включая старые `log.Printf` вызовы, идут в файл и не теряются при перезапуске демона.

## Где лежат логи

Каждый запуск получает свой файл:

```
<project-state>/logs/beacon-<UTC timestamp>-<pid>.log
<project-state>/logs/beacon.latest.log -> beacon-<UTC timestamp>-<pid>.log
```

`<project-state>` выбирается по проекту: это `<XDG_STATE_HOME>/beacon/projects/<name>-<hash>`, когда в текущей папке есть `.git`. Явный override задаётся переменной `BEACON_STATE_DIR`. Точный путь подсказывает `beacon doctor`.

Примеры чтения:

```bash
STATE="${BEACON_STATE_DIR:-$HOME/.local/state/beacon/projects/<name>-<hash>}"
ls -lt "$STATE/logs" | head
tail -f "$STATE/logs/beacon.latest.log"
```

`beacon.latest.log` это симлинк на файл последнего запуска. После рестарта симлинк переключается на новый файл, старые файлы остаются на диске. Имена содержат время в UTC и pid, поэтому два запуска с одинаковой меткой времени получают разные файлы.

## Как читать записи

Формат по умолчанию текст slog. У каждой записи есть поле `event`, остальные поля зависят от события:

```
time=... level=INFO event=verdict_published verdict=ready watchID=watch:... pr=123
```

JSON включается переменной окружения:

```bash
BEACON_LOG_FORMAT=json beacon daemon --once
BEACON_LOG_FORMAT=json beacon metrics --format json
tail -f "$STATE/logs/beacon.latest.log" | jq 'select(.event == "cycle")'
```

Уровень логирования задаётся `BEACON_LOG_LEVEL` со значениями `debug`, `info` (по умолчанию), `warn`, `error`. Отладочные события `poll_cycle_start`, `poll_cycle_end`, `deps_resolved`, `provider_call` видны только на `debug`.

Раз в проход демон пишет одну компактную INFO-строку `cycle` с полями `watches`, `ready`, `retry`, `merges`, `wakes`, `duration`. Это первый вопрос, который стоит задать: проход что-то сделал или проходил вхолостую.

## Метрики

```bash
beacon metrics --format json
beacon metrics --format prometheus
```

Счётчики считаются в памяти одного процесса. Демон показывает свои нарастающие итоги, короткая команда только то, что она сама записала.

| Метрика | Что считает |
| --- | --- |
| `poll_cycles_total` | запусков опроса CI |
| `verdict_published_total{verdict}` | опубликованных вердиктов, с меткой `ready`/`retry`/`fail` |
| `merge_attempts_total` | попыток автоматического мержа |
| `merge_success_total` | успешных мержей |
| `merge_failures_total` | отказов мержа |
| `wake_delivered_total{adapter}` | доставленных wake по адаптеру |
| `wake_errors_total{adapter}` | ошибок доставки wake по адаптеру |
| `wake_skipped_total` | wake, намеренно не отправленных, с причиной в логе |
| `rebase_requests_total` | указаний подписчику сделать rebase |
| `dependency_waits_total` | задержек из-за неудовлетворённых `waitFor` |
| `blocks_total` | обнаруженных блокировок Watch |
| `actor_errors_total` | ошибок акторов демона и обработчиков |

Гистограммы времени: `poll_cycle_duration_seconds`, `merge_duration_seconds`, `wake_duration_seconds`, `run_duration_seconds`. В Prometheus они экспортируются обычными наборами `_bucket{le=...}`, `_sum`, `_count`.

## Отладка: PR зелёный, но не замержился

Пройдите по событиям одного прохода в порядке:

1. `poll_cycle_start` и `poll_cycle_end`: видно ли вообще этот Watch и опрашивался ли CI. Watch без подписчика и закрытый Watch пропускаются.
2. `verdict_published` с `verdict=ready`. Если вердикт `retry`, причина в CI, а не в мерже.
3. `deps_resolved` или `dependency_wait`. Проверка `waitFor` и `waitForMain` может удержать зелёный PR. Счётчик `dependency_waits_total` покажет, как часто это происходит.
4. `blocked_detected`. Заблокированный Watch не опрашивается и не мержится, пока не придёт разблокировка. Счётчик `blocks_total`.
5. `merge_attempt`, затем `merge_success` или `merge_fail`. Если `merge_attempt` нет при `verdict=ready`, причина в политике `autoMerge` или в удержании зависимостей. Если есть `merge_fail`, смотрите поле `error`.

Быстрая проверка по метрикам:

```bash
BEACON_LOG_FORMAT=json beacon metrics --format json | jq '{verdicts: .verdict_published_total, attempts: .merge_attempts_total, failures: .merge_failures_total, deps: .dependency_waits_total, blocks: .blocks_total}'
```

Если `verdict_published_total.ready` больше `merge_attempts_total`, мерж не начинался. Если `merge_attempts_total` больше `merge_success_total`, смотрите `merge_failures_total` и события `merge_fail`.

## Отладка: wake не дошёл

Ключевые события в порядке доставки:

1. `watch_registered`: Watch создан с подписчиком и адаптером.
2. `verdict_published`: вердикт не terminal. Для terminal-вердикта Watch закрывается, и wake по замыслу не отправляется.
3. `wake_skipped`: wake намеренно пропущен, причина в поле `reason`. Частые причины: нет подписчика, адрес obligation не совпадает с подписчиком, невалидный sessionID.
4. `wake_delivered` с полем `adapter`: хост принял запрос.
5. `wake_error` с полями `adapter`, `error`: транспорт или хост отказали.
6. `rebase_requested`: оркестратор попросил подписчика сделать rebase. Если после него нет `wake_delivered`, проверяйте `wake_skipped` и `wake_error`.

```bash
tail -f "$STATE/logs/beacon.latest.log" | jq 'select(.event | test("wake|rebase|verdict"))'
BEACON_LOG_FORMAT=json beacon metrics --format json | jq '{delivered: .wake_delivered_total, errors: .wake_errors_total, skipped: .wake_skipped_total, rebase: .rebase_requests_total}'
```

Адрес wake всегда берётся из `subscriber.ID` Watch. Старые Watch, где в качестве цели записан сервисный 이름 вместо sessionID, пропускаются: в логе будет `wake_skipped`, в метрике `wake_skipped_total`. Такой Watch нужно перерегистрировать валидным `--wake-target`.

## Что делать, если лог пуст

Проверьте, что команда действительно запускалась с тем же `<project-state>`, который вы читаете. Beacon привязан к проекту через `.git` и `BEACON_STATE_DIR`. Если каталог состояния недоступен, логгера нет, об ошибке пишется одна строка в stderr.
