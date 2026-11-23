# Observability: логи и метрики Beacon

Beacon пишет структурированные логи в файл и держит счётчики в памяти процесса. stderr используется только для ошибок настройки логгера. Обычные записи, включая старые `log.Printf` вызовы, идут в файл и не теряются при перезапуске демона.

Код живёт в пакете `internal/infra/observe`. Это листовой фасад без внутренних зависимостей, поэтому в матрице слоёв он занимает отдельную строку с рангом 0. Импортировать его могут `internal/infra`, `internal/app` и `cmd`. Соответствующие рёбра помечены `shared`: гейт проверяет их направление, но не требует, чтобы у фасада был вызывающий в каждом дереве.

## Переменные окружения наблюдаемости

Полная таблица production-переменных лежит в `README.md`, раздел «Конфигурация: переменные окружения». Это один документ для оператора. Здесь перечислены только переменные, которые влияют на логи, метрики и трассировку.

| Переменная | По умолчанию | Что меняет |
| --- | --- | --- |
| `BEACON_STATE_DIR` | каталог проекта по `.git` | явный каталог состояния, в нём лежат логи |
| `XDG_STATE_HOME` | `~/.local/state` | базовый каталог для каталога состояния |
| `BEACON_LOG_FORMAT` | `text` | `json` включает структурированные записи для `jq` |
| `BEACON_LOG_LEVEL` | `info` | `debug` показывает события `poll_cycle_start`, `poll_cycle_end`, `deps_resolved`, `provider_call` |
| `BEACON_METRICS_PORT` | пусто | открывает HTTP endpoint `/metrics` на `127.0.0.1` |
| `BEACON_OTEL_ENABLED` | `false` | включает экспорт трейсов, только литерал `true` |
| `BEACON_OTEL_EXPORTER` | пусто | `otlp` включает OTLP, любая опечатка молча даёт local sink |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | пусто | адрес OTLP-экспортера, нужен scheme и host |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | пусто | перекрывает базовый endpoint для трейсов |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | `grpc` отвергается экспортером и даёт local fallback |

Два поведения стоит помнить. `BEACON_LOG_LEVEL` с неверным значением пишет warning в stderr в CLI, но `OpenServerLog` молча берёт `info`. `BEACON_METRICS_PORT` при неверном значении пишет ошибку в stderr и не роняет команду, поэтому endpoint просто не откроется.

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

Команда `beacon metrics` печатает снимок в stdout. Отдельный HTTP endpoint `/metrics` поднимается только при заданном `BEACON_METRICS_PORT` и слушает `127.0.0.1`. Пример: `BEACON_METRICS_PORT=9090 beacon daemon`. Если порт не задан или неверный, endpoint не откроется, а команда продолжит работу. Полные значения и валидация перечислены в таблице `README.md`.

| Метрика | Что считает |
| --- | --- |
| `poll_cycles_total` | запусков опроса CI |
| `verdict_published_total{verdict}` | опубликованных вердиктов; метка `verdict` это доменный вердикт из `internal/domain/types.go` |
| `merge_attempts_total` | попыток автоматического мержа |
| `merge_success_total` | успешных мержей |
| `merge_failures_total` | отказов мержа |
| `wake_delivered_total{adapter}` | доставленных wake по адаптеру |
| `wake_errors_total{adapter}` | ошибок доставки wake по адаптеру |
| `wake_skipped_total` | wake, намеренно не отправленных, с причиной в логе |
| `rebase_requests_total` | указаний подписчику сделать rebase |
| `dependency_waits_total` | задержек из-за неудовлетворённых `waitFor` |
| `blocks_total` | обнаруженных блокировок Watch |
| `blocks_escalated_total` | блокировок, записанных `blocked:not_mine` (считается только первый ack) |
| `fixers_spawned_total` | FIX-агентов, запущенных для снятия блокировки |
| `actor_errors_total` | ошибок акторов демона и обработчиков |

Метка `verdict` берётся из перечисления `domain.Verdict` в `internal/domain/types.go`: `watching`, `ready`, `rebase`, `retry`, `keep_working`, `timeout`, `merged`, `closed`, `blocked`, `recheck`. Других значений в метрике не бывает. В частности `fail` в неё не пишется: это внутреннее решение оркестратора (`orchestrator.VerdictFail`), а не доменный вердикт.

Гистограммы времени: `poll_cycle_duration_seconds`, `merge_duration_seconds`, `wake_duration_seconds`, `run_duration_seconds`. В Prometheus они экспортируются обычными наборами `_bucket{le=...}`, `_sum`, `_count`.

## Метрика предотвращённых PR-дублей (CLS-04)

Метрика считает карточки, у которых проверка premise остановила открытие кодового PR. Основной источник это карточка `docs/tasks/<CARD>.md`, поля «Причина» и «Время закрытия». Второй источник это вердикт скрипта CLS-01 для кодовых PR, когда скрипт появится в CI. Код `internal/infra/observe` не меняется: у счётчика нет производителя внутри процесса, а нулевая серия в `/metrics` читалась бы как отсутствие предотвращённых дублей.

Машиночитаемое определение:

```yaml
premise_duplicate:
  name: beacon_premise_duplicate_avoided_total
  type: counter
  help: Premise outcomes that stopped a duplicate code PR
  source: card-or-ci
  idempotency_key: [cardID, sha]
  card_id_in_labels: false
  labels:
    outcome: [already_landed, duplicate, owner_decision, false_premise]
  excluded_outcomes: [clean]
```

Единственная метка `outcome` принимает четыре значения. `cardID` остаётся в карточке и в записи проверки, но не в метрике. Число рядов поэтому не растёт с числом карточек.

| Исход CLS-04 | Вердикт CLS-01 | Причина в `CLOSE-PROCEDURE.md` | Маркер в поле «Причина» карточки |
| --- | --- | --- | --- |
| `already_landed` | `landed` | уже влито | `already landed`, `Уже влито`, `merge-base --is-ancestor` |
| `duplicate` | `duplicate` | дубль или чужой PR | `duplicate`, `дубль` |
| `owner_decision` | нет | решение владельца | `owner decision`, `решение владельца` |
| `false_premise` | нет | ложная premise | `false premise`, `ложная premise` |
| `clean` | `clean` | карточка не закрывается | в счётчик не входит |

CLS-01 различает только кодовые PR и даёт вердикты `clean`, `landed`, `duplicate`. Два других исхода приходят из таблицы причин `CLOSE-PROCEDURE.md`. Все четыре исхода имеет один источник: карточка или CI-проверка.

Идемпотентность держится на ключе `(cardID, sha)`. SHA это первый набор из 40 шестнадцатеричных символов в полях «Причина» и «Доказательство». Если SHA нет, ключ равен `cardID`. Повторный замер той же карточки с тем же SHA ряд не увеличивает.

Замер за сутки:

```bash
git fetch origin main
CUTOFF=$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ)
rg -N '^Причина:|^Время закрытия:' docs/tasks/ \
| awk -v cutoff="$CUTOFF" '
  {
    p = index($0, ":"); path = substr($0, 1, p-1)
    rest = substr($0, p+1)
    q = index(rest, ":"); field = substr(rest, 1, q-1); value = substr(rest, q+1)
    sub(/^ /, "", value)
    card = path; sub(/^docs\/tasks\//, "", card); sub(/\.md$/, "", card)
    if (field == "Время закрытия") closed[card] = value; else reason[card] = value
  }
  END {
    for (card in reason) {
      if (!(card in closed) || closed[card] < cutoff) continue
      r = tolower(reason[card])
      if (r ~ /already[_ ]landed|уже влито|уже в main|fix already merged|merge-base/) o = "already_landed"
      else if (r ~ /duplicate|дубл/) o = "duplicate"
      else if (r ~ /owner decision|решение владельца/) o = "owner_decision"
      else if (r ~ /false premise|ложная premise/) o = "false_premise"
      else continue
      sha = "none"
      if (match(r, /[0-9a-f]{40}/)) sha = substr(r, RSTART, RLENGTH)
      print o "\t" card "\t" sha
    }
  }' \
| sort -u | cut -f1 | sort | uniq -c
```

Команда печатает число строк по каждому исходу. `sort -u` убирает повторы по ключу `(cardID, sha)` до подсчёта. Запускайте её в чистом чекауте `origin/main`: чтение идёт из рабочего дерева `docs/tasks/`.

Baseline: замер 2026-11-23 на `79dc1694` с окном 24 часа дал 377 строк `already_landed`, 9 строк `duplicate` и 1 строку `false_premise`. Итого 387 предотвращённых дублей. Исход `clean` в счётчик не входит.

## Бюджет памяти: предупреждение RSS и уборка сессий

Бюджет памяти задан ADR 0191 и карточкой BS.51. Один процесс должен держать менее 100 MB резидентной памяти, когда активны 15 и более Watch. Пороги лежат в `internal/infra/server/metrics.go`: `FootprintWatchThreshold = 15` и `FootprintLimitBytes = 100 * 1024 * 1024`. Проверка `FootprintCheck` предупреждает только при обоих условиях. Меньше 15 Watch или ровно 100 MB предупреждения не дают.

Предупреждение пишется в лог одной строкой:

```
WARNING: RSS %d bytes exceeds %d bytes at %d active watches
```

Первое число это текущий RSS процесса, второе предел в байтах, третье число активных Watch. Значение RSS приходит из `VmRSS` в `/proc/self/status`, его читает `server.ProcessRSSBytes`. На системе без procfs чтение возвращает false и предупреждения нет. Проверить текущее значение можно напрямую:

```bash
grep VmRSS /proc/<pid>/status
```

Гейты SLO используют `VmHWM`, пик за время жизни процесса. Здесь речь о текущем `VmRSS`, поэтому числа могут расходиться с таблицами в `docs/perf/README.md`.

Метрика в коде называется `rss_bytes`, в формате Prometheus она экспортируется как `beacon_rss_bytes`. У неё нет меток, поэтому она даёт ровно одну серию при любом числе Watch и PR.

Порядок действий при предупреждении:

1. Сверьте число из предупреждения с текущим RSS процесса. Для этого подставьте pid демона в команду выше. Убедитесь, что читаете лог того процесса, который держит Watch.
2. Посмотрите активные Watch командой `beacon watch --format json`. В ответе видны watchID, headSHA и sessionID подписчика. Сравните число Watch с полем `at %d active watches` в предупреждении.
3. По headSHA найдите PR и сессию каждого Watch. `beacon inspect <watchID>` покажет состояние и блокеры.
4. Запустите `beacon gc --format json` без `--apply`. Dry run печатает план и не меняет состояние. В отчёте поле `mode` равно `dry-run`, а `items` содержат каждую запись с видом, идентификатором и причиной.
5. Убедитесь, что план не содержит сессий активных Watch. Проход GC исключает sessionID из `LiveWatches` и текущую сессию. Если сессия живого Watch попала в план, остановитесь и проверьте реестр LiveWatches.
6. Примените уборку командой `beacon gc --apply --format json`. Отчёт переходит в режим `applied` и перечисляет удалённые записи. Сравните число в поле `removed` с планом.

Удаление сессии закрытого Watch и ошибка 404 это разные события. GC трогает только сессии закрытых Watch, которые прошли окно удержания. Строка отчёта содержит sessionID, watchID закрытого Watch и причину закрытия. Это штатная уборка, и повторный dry run её не покажет.

Ошибка 404 приходит из wake-пути, когда сессия живого Watch не найдена. `internal/infra/wake/deliver.go` превращает её в `wake: session dead` (`ErrSessionDead`). Это не уборка, а потеря цели Wake. Проверьте `wake_errors_total`, события `wake_error` и `wake_skipped`, затем адрес подписчика в Watch. Живой Watch с мёртвой сессией требует перерегистрации подписчика, а не повторного GC.

Перед изменением кода, которое должно снизить RSS, снимите baseline и запишите число в поле `baseline` карточки. AGENTS.md требует измерения до выбора подхода. Baseline это снимок `rss_bytes` при известном числе активных Watch, снятый до правки. После правки повторите замер при том же числе Watch и сравните значения.

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

## Отладка: blocked → fixer → unblock

Цепочка из четырёх шагов. Первые два оставляют собственные события в логе, остальные видны по состоянию Watch и вердикту. По порядку фактов видно, где цепочка прервалась.

1. **blocked.** `beacon_ack` с `decision=blocked:not_mine` пишет `block_escalated` с полями `watchID` и `reason` и увеличивает `blocks_escalated_total`. В payload Watch появляется объект `blocked` с полями `reason`, `failedChecks`, `blockedBy` и `since`; статус и вердикт становятся `blocked`. Следующий проход моста добавляет `blocked_detected` и увеличивает `blocks_total`. Повторный ack ничего не меняет: `blocked` и ревизия остаются прежними, `blocks_escalated_total` не растёт.
2. **fixer.** Оркестратор спавнит FIX-агента по wake на ревизии блокировки. Точка спавна вызывает `observe.FixerSpawned`: в логе появляется `fixer_spawned` с полем `watchID`, счётчик `fixers_spawned_total` растёт. Если `blocks_escalated_total` растёт, а `fixers_spawned_total` нет, проверьте `wake_delivered`, `wake_skipped` и `wake_errors_total`: wake до оркестратора не дошёл или спавн не был вызван.
3. **unblock.** После того как фикс влит и проверки зелёные, разблокировка (`blocked:resolved`, доменный `UnblockEvent`) очищает объект `blocked`, возвращает статус и вердикт `watching` и увеличивает ревизию. Отдельного счётчика у этой операции нет: фактом служит отсутствие `blocked` в `beacon_get_result` и выросшая ревизия.
4. **разблокировка.** Следующий тик моста публикует `verdict_published` с `verdict=ready`, уходит `wake_delivered`, затем `merge_attempt` и `merge_success`. Успешный мерж закрывает Watch.

```bash
tail -f "$STATE/logs/beacon.latest.log" | jq 'select(.event | test("block_escalated|blocked_detected|fixer_spawned|verdict_published|merge_success"))'
BEACON_LOG_FORMAT=json beacon metrics --format json | jq '{escalated: .blocks_escalated_total, detected: .blocks_total, fixers: .fixers_spawned_total, delivered: .wake_delivered_total, errors: .wake_errors_total}'
```

Если после `block_escalated` нет `blocked_detected`, заблокированный Watch не попал в следующий проход моста. Если есть `blocked_detected`, но нет `fixer_spawned`, смотрите доставку wake по разделу выше.

## Что делать, если лог пуст

Проверьте, что команда действительно запускалась с тем же `<project-state>`, который вы читаете. Beacon привязан к проекту через `.git` и `BEACON_STATE_DIR`. Если каталог состояния недоступен, логгера нет, об ошибке пишется одна строка в stderr.
