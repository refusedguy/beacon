# E04_executor_state_visible_through_seven_tools

## Сценарий

Один executor в работе, второй ждёт, третий зависит от оркестратора с parentID.

## Проверки

- beacon_stats содержит byExecutorState с тремя состояниями
- beacon_list_running показывает state и parentSessionID у подписчиков
- tools/list возвращает ровно семь инструментов

## Негативный контроль

Добавление восьмого инструмента ломает contract_test.
