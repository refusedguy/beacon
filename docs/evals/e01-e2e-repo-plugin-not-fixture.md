# E01_e2e_repo_plugin_not_fixture

## Сценарий

runWakePositive поднимает opencode serve и грузит .opencode/plugins/beacon-bridge.js вместо tests/e2e/fixtures/beacon-bridge.js. Строка в events.jsonl приходит от продового адаптера или от драйвера postWakePointer без подмены обработчика.

## Проверки

- verifyWake видит prompt.ok delivery=steer от репозиторийского плагина
- В логе есть ctx-event session.execution.started
- repo_plugin_skeleton содержит assert, а не только вывод в лог
- frozenScenarioHash обновлён вместе с таблицами e2e README

## Негативный контроль

fixtures/beacon-bridge-stub.js по-прежнему не проходит verifyWake. Эталон fixtures/beacon-bridge.js больше не служит источником подтверждения пробуждения.
