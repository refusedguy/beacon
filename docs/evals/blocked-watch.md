# Blocked Watch: eval-контракт B01-B05

Пять локальных проверок фиксируют поведение Watch, который исполнитель
заблокировал после разбора красных проверок. Исполнитель сравнивает failedChecks
со своим diff и шлёт `beacon_ack` с `decision=blocked:not_mine` и причиной. Watch
переходит в `blocked`, CI-мост его пропускает, оркестратор получает wake. Явный
`blocked:resolved` возвращает Watch в обычный опрос. Отдельный кейс B05
различает plain ack и blocked: plain ack только ставит `claimedAt` и оставляет
wake демона активным, blocked останавливает wake исполнителю и уходит по маршруту
оркестратора.

Проверки живут в `evals/main.go` и выводятся в общей таблице раннера. Запуск:

```bash
GO=/tmp/opencode/go126/bin/go bash evals/run.sh
```

Прогон не требует сети, токенов GitHub, MCP-сервера и node. Раннер использует
уже существующую инфраструктуру evals: общий вызов `record`, общую таблицу
`NAME | EXPECTED | ACTUAL | PASS/FAIL` и общий код выхода.

## Сценарии

### B01_block_decision

Вход: Watch в `watching`, `decision=blocked:not_mine`, причина `правки моих нет, проверил diff`.

Проверки:

- исход ack содержит действие `blocked` и ту же причину;
- Watch имеет статус `blocked` и сохранённую причину;
- негативный контроль: `decision=approve` не меняет статус и возвращает
  `unknown_decision`.

Ожидаемый `ACTUAL`: `block=blocked/<причина> status=blocked reason=<причина>;
approve=noop/unknown_decision status=watching`.

### B02_ci_bridge_skips_blocked

Вход: Watch с двумя красными проверками, заблокированный по B01.

Проверки:

- один проход CI-моста по заблокированному Watch даёт ноль опросов и ноль retry;
- негативный контроль: тот же Watch до блокировки даёт один опрос и один retry.

Ожидаемый `ACTUAL`: `blocked: polls=0 retries=0; watching: polls=1 retries=1`.

### B03_blocked_wake

Вход: заблокированный Watch с причиной `не моя вина` и failedChecks
`ci-build`, `ci-audit`.

Проверки:

- wake для оркестратора несёт `watchID`, PR в форме `repo#number`, причину и
  failedChecks;
- failedChecks отсортированы, порядок в store не влияет на wake;
- негативный контроль: незаблокированный Watch не порождает wake.

Ожидаемый `ACTUAL`: `watch=watch:B03, pr=refusedguy/beacon#299, reason=не моя
вина, failedChecks=[ci-audit ci-build], watchingWake=false`.

### B04_unknown_and_unblock

Вход: Watch в `watching`, затем три последовательных решения.

Проверки:

- `decision=blocked:banana` с неизвестным reason возвращает `unknown_reason`,
  статус остаётся `watching`, опрос продолжается;
- `decision=banana` без префикса возвращает `unknown_decision` и тоже не
  блокирует;
- `decision=blocked:not_mine` блокирует Watch;
- `decision=blocked:resolved` возвращает статус `watching`, очищает причину, и
  следующий проход CI-моста снова даёт один опрос и один retry.

Ожидаемый `ACTUAL` содержит `unknown=noop/unknown_reason`, `alien=noop/unknown_decision`
и `unblock=unblocked status=watching reason="" polls=1 retries=1`.

### B05_plain_ack_vs_blocked_wake

Вход: Watch в `watching` с активным wake. Три шага подряд: plain ack с
obligationID, `blocked:not_mine` с причиной `правки моих нет`, затем
`blocked:resolved`.

Проверки:

- plain ack ставит `claimedAt`, не меняет статус и не глушит wake демона;
- негативный контроль: plain ack не останавливает wake;
- `blocked:not_mine` останавливает wake исполнителю, но шлёт wake оркестратору;
- `blocked:resolved` возвращает wake демона.

Ожидаемый `ACTUAL`: `plain=acked/claimed claimed=true status=watching wake=true;
blocked=blocked/правки моих нет status=blocked wake=false orchestratorWake=true;
resolved=unblocked status=watching wake=true`.

## Что именно проверяется

Модель в `evals/main.go` описывает только наблюдаемые исходы. `applyBlockDecision`
возвращает новый статус Watch и действие ack. `ciBridgeRefresh` считает опросы и
retry за один проход моста. `blockedWakeFor` собирает wake для оркестратора или
возвращает `nil`. `plainAck` ставит `claimedAt` и ничего больше. `daemonWakeAllowed`
показывает, что демон ещё может слать wake исполнителю. Eval сравнивает эти
значения с ожиданием, записанным в коде. Внутренние вызовы и промежуточные
переменные не проверяются. Поведенческие тесты тех же функций лежат в
`evals/blocked_test.go` под build tag `eval`.

Каждый негативный контроль живёт в том же кейсе, что и позитивный путь. Если убрать
проверку статуса в B01, пропуск в B02, поля wake в B03, обработку неизвестного
decision в B04 или различие двух ack в B05, соответствующий кейс становится
красным и раннер возвращает код 1.

## Граница модели

Production-код блокировки теперь есть в main: `beacon_ack` понимает
`blocked:not_mine` и `blocked:resolved`, CI-мост пропускает `StatusBlocked`,
демон разделяет маршруты wake исполнителю и оркестратору. B01-B05 всё ещё
проверяют исполняемую модель контракта, как W14-W18 проверяют модель ADR 0072.
Модель не подменяет реальный обработчик и не выдаёт заранее верный ответ: она
описывает наблюдаемые исходы, которые обязаны воспроизвести реальные
`beacon_ack`, мост и доставка wake. Живой зонд `evals/run.sh` поднимает
MCP-сервер с SQLite, его результат проверяется в CI. Перенос кейсов на настоящий
код сохранит имена и негативные контроли.
