# ATX-V04. Informational-only снимок с ERROR rollup не даёт keep_working
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/domain/verdict.go, internal/domain/evaluate_pr_test.go, internal/domain/verdict_test.go
Зависимости: ATX-V01
Ссылка: docs/research/verdict-contract-gaps.md, раздел 4
Агент: нет
Время: нет
Цель: Закрыть путь к keep_working, который обходит фильтр информационных проверок через rollup ERROR и движок Evaluate.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 4. В `EvaluatePR` восстановление rollup в verdict.go:236-249 работает только при `rollup == "FAILURE"`. Снимок с `Rollup: "ERROR"` и одними optional-сбоями доходит до строки 250 и даёт `keep_working`. `buildRollup` в github/errors.go:439-464 умеет возвращать `ERROR`, а `StatusResult.Apply` (:336) пишет его в снимок. `Evaluate` (:139-150) не фильтрует `Required` вообще. Baseline: один не закрытый тип rollup и один движок без фильтра.
Шаги: Распространить восстановление rollup на `ERROR` по той же схеме, что и на `FAILURE`. В `Evaluate` отфильтровать informational-сбои до строк решения 5-6, оставив fail-closed для проверок без карты. Добавить табличные тесты в evaluate_pr_test.go и verdict_test.go.
Проверка: CI зелёный. Снимок с `Rollup: "ERROR"`, одной optional FAILURE и валидными merge-гейтами не даёт `keep_working` и не будит executor. Такой же снимок для `Evaluate` не даёт `keep_working`.
Приёмка: Ни один из двух движков не возвращает `keep_working`, когда блокирующих проверок нет.
