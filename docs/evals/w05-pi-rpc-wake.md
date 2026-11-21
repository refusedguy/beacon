# W05_pi_rpc_wake

## Сценарий

Pi RPC wake доставляет указатель через выбранный RPC-метод. Живого Pi-процесса в этой среде нет, поэтому проверка адаптера идёт на подставном провайдере. Живой переход `get_state` из idle в processing и рост JSONL остаются за отдельной карточкой T3.227.

## baseline

Замер на `c9a0e3f` (origin/main): `git grep -c -E 'PiProbe|PiRPC' HEAD -- internal/infra/wake/registry.go cmd/beacon/deliver.go` дал 0 совпадений. Тестов мокового провайдера в `cmd/beacon/deliver_test.go` было 0. В `evals/main.go` работала одна проверка `21_w05_pi_honest_unsupported`, которая фиксирует честный unsupported.

## Проверка адаптера

CI запускает `go test ./cmd/beacon -run 'TestPiRPCProbe|TestDeliverPiRPC|TestDeliverPi'`. Тесты подключают моковый `wake.PiProbe` и моковый `wake.PiRPCConnection` через слот `resolvePiProbe` в `cmd/beacon/deliver.go`. Реальный процесс не создаётся.

- Успешный RPC: конфиг `BEACON_DELIVER_ADAPTER=pi` вызывает `followUp` по умолчанию и даёт `delivery=queue outcome=queue attempts=1`. Режим `BEACON_DELIVER_PI_MODE=steer` вызывает `steer` и даёт `delivery=steer outcome=steer`.
- Ошибка провайдера: `rpc failed` даёт `outcome=indeterminate delivery=unsupported` и один вызов без ретраев.
- Таймаут: `BEACON_DELIVER_PI_TIMEOUT=1ms` и `context.DeadlineExceeded` дают `outcome=transport-failed`, три попытки по ADR 0017 и `delivery=unsupported`.
- Фолбэк: пустой или чужой адаптер, битый конфиг или отсутствие провайдера оставляют текущий `deliveryTarget`. Незаданный target по-прежнему даёт честный unsupported.

Проверка провайдера на моковом соединении сверяет имена методов `followUp` и `get_state`, sessionID и текст указателя. Это показывает, что RPC-вызовы идут через внедрённый transport, а не через созданный процесс.

## Проверки

Эти пункты остаются непроверенными до появления Pi-песочницы:

- get_state показывает переход idle -> processing или растёт JSONL сессии
- Guard extension не блокирует RPC-доставку
- steer и followUp различаются в отчёте живого прогона

## Негативный контроль

Заблокировать tool через guard extension. Доставка не должна зависеть от блокировки инструмента. Для адаптера негативный контроль уже есть: ошибка и таймаут провайдера не печатаются как доставка.
