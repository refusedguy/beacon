# W03_steer_vs_queue

## Сценарий

Две задачи: steer и queue. Сессия продолжает работать, затем наступает idle boundary.

## Проверки

- steer разбирается на Safe Step Boundary, не в конце очереди
- При idle steers имеют приоритет перед queue
- interrupt?resume=true возобновляет steer и control items, queue остаётся припаркованной

## Негативный контроль

Только queue плюс interrupt?resume=true: задача остаётся припаркованной, выполнение не возобновляется.
