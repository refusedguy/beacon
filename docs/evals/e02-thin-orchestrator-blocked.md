# E02_thin_orchestrator_blocked

## Сценарий

Executor получает падение чужой проверки и шлёт blocked:not_mine; оркестратор просыпается и запускает FIX-агента.

## Проверки

- оркестратор будится через blocked:not_mine
- FIX-агент создаётся через beacon task new и beacon spawn
- оркестратор не чинит main сам
- Beacon только наблюдает и доставляет wake

## Негативный контроль

Executor не трактует blocked:not_mine как retry своего PR и не переделывает код.
