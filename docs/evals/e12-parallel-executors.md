# E12_parallel_executors

## Сценарий

Поднять 20 stub executor и доставить 100 wake с задержками доставки.

## Проверки

- Все wake доставлены
- Нет дублей по clientRequestId
- active_tasks возвращается в ноль

## Негативный контроль

Снижение concurrency до одного executor ломает сценарий по времени.
