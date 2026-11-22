# ATX-V07. Свести нормализацию заключений проверок к одной таблице
Статус: done
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

Итог: Cardindex resolves done: OwnerFiles overlap with T1.5 (already done). Supporting merged commit d452e555db69 (PR #1617) touches the card OwnerFiles.
Доказательство: Commit d452e555db692dfbdcb81f7807902e942b846f82 (PR #1617, fix(mcp): surface failure excerpt fetch errors (EH-02) (#1617)) touches OwnerFiles internal/domain/verdict.go, internal/domain/verdict_test.go, internal/infra/cibridge/ci_refresh.go, internal/infra/github/errors.go, internal/infra/mcp/tool_get_result.go; git merge-base --is-ancestor d452e555db692dfbdcb81f7807902e942b846f82 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1617 d452e555db69
Время закрытия: 2026-11-22T11:27:01Z
