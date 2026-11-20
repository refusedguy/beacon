# W22_cross_host_successor_first

## Сценарий

Cross-host handoff с crash после создания successor на целевом хосте.

## Проверки

- Successor жив на целевом хосте
- Повторная передача поглощает тот же id
- Source закрыт с handed_off_to

## Негативный контроль

Закрыть source до создания successor и показать потерянное обязательство.
