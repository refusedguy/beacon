# W19_required_checks

## Сценарий

Конфиг задаёт requiredChecks build, test, gates и ignoreChecks e2e. PR имеет SUCCESS для build и test, FAILURE для gates, FAILURE для e2e.

## Проверки

- FAILURE обязательной проверки gates даёт verdict retry
- если gates SUCCESS и все обязательные SUCCESS, вердикт ready
- FAILURE только в ignoreChecks не меняет вердикт
- отсутствие обязательной проверки в PR блокирует ready

## Негативный контроль

Убрать requiredChecks из конфига. Верификатор возвращает ready при FAILURE gates, и прогон обязан упасть.
