# R05_stop_precondition_changed

## Сценарий

Перед stop сеанс пересоздан

## Проверки

- Отказ selection_changed
- kill не отправлен
- stopPhase отсутствует

## Негативный контроль

Совпавший precondition разрешает stop
