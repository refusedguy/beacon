# W05_pi_rpc_wake

## Сценарий

Pi RPC wake. get_state показывает переход из idle в processing, либо растёт JSONL сессии. Сценарий это непроверенный спайк: живого wake через Pi в отчётах нет.

## Проверки

- get_state показывает переход idle -> processing или растёт JSONL
- Guard extension не блокирует RPC-доставку
- steer и followUp различаются в отчёте

## Негативный контроль

Заблокировать tool через guard extension. Доставка не должна зависеть от блокировки инструмента.
