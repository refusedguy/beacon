# ATX-V07. Свести нормализацию заключений проверок к одной таблице
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/domain/verdict.go, internal/infra/cibridge/ci_refresh.go, internal/infra/mcp/tool_get_result.go, internal/infra/github/errors.go, internal/domain/verdict_test.go
Зависимости: ATX-V01, ATX-V04
Ссылка: docs/research/verdict-contract-gaps.md, раздел 7
Агент: нет
Время: нет
Цель: Обеспечить одинаковую классификацию заключений проверки во всех путях, которые влияют на вердикт и на failedChecks.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 7. Четыре функции задают разные множества. `NormalizeConclusion` в verdict.go:300-311 знает PASS, SUCCESSFUL, FAIL и ACTION_REQUIRED. `checkFacts` в ci_refresh.go:100-121 знает STARTUP_FAILURE, но не знает этих aliases. `failureConclusions` и `isFailedCheck` в tool_get_result.go:44-54 и :936-947 знают STARTUP_FAILURE и CANCELLED, но не ACTION_REQUIRED. `buildRollup` в github/errors.go:439-464 работает только с CheckState. Baseline: четыре таблицы и как минимум три расхождения в множествах.
Шаги: Вынести каноническую таблицу в domain и использовать её во всех четырёх местах. Для неизвестного значения оставить fail-closed, как сейчас в `NormalizeConclusion`. Обновить табличный тест на всех aliases и проверить, что результат одинаков в доменном вердикте и в проекции `failedChecks`.
Проверка: CI зелёный. Для каждого aliases из таблицы тест ожидает одно и то же каноническое состояние во всех четырёх функциях. ACTION_REQUIRED и STARTUP_FAILURE покрыты явно.
Приёмка: Изменение таблицы заключений происходит в одном файле и сразу меняет все пути.
