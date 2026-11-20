# E01_ci_processes_per_tick

## Сценарий

15 активных Watch, один проход cibridge.Refresh, полная нагрузка.

## Проверки

- pgrep -fc 'gh pr' равен нулю во время прохода
- запущен один процесс Beacon
- httptest-сервер GitHub видит ровно один POST /graphql за проход

## Негативный контроль

Базовый ghci.Provider с теми же 15 Watch запускает 30 процессов gh за проход.
