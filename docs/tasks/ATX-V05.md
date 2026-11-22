# ATX-V05. Убрать дубль движка вердиктов из checkall
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/orchestrator/checkall.go, internal/app/orchestrator/checkall_test.go, internal/app/orchestrator/checkall_contract_test.go
Зависимости: ATX-V01
Ссылка: docs/research/verdict-contract-gaps.md, раздел 5, ADR 0005
Агент: нет
Время: нет
Цель: Не давать параллельному движку вердиктов публиковать retry по информационной проверке.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 5. `VerdictFromChecks` в checkall.go:442-462 повторяет решение EvaluatePR, но не читает `Required`. Optional FAILURE даёт `CheckVerdictRetry`, который `checkall` публикует в Watch (:421-433). `FailedChecks` (:465-474) собирает те же имена без фильтра. Слой app не может импортировать domain по ADR 0005, поэтому значения вердиктов уже дублируются в checkall.go:29-52. Baseline: два движка, один без поля Required.
Шаги: Ввести узкий порт, который принимает список проверок и возвращает только факт наличия блокирующего сбоя и его имена. Composition root связывает порт с доменной функцией из ATX-V01. Перевести `VerdictFromChecks` и `FailedChecks` на порт. Обновить тест равенства зеркал вердиктов.
Проверка: CI зелёный. Список только с optional FAILURE даёт `watching`, `FailedChecks` пуст и публикации нет. Список с required FAILURE даёт `retry` и имя в `FailedChecks`.
Приёмка: checkall и cibridge принимают одинаковое решение по одному и тому же набору проверок.

Итог: Cardindex resolves done: OwnerFiles overlap with HD-024 (already done). Supporting merged commit 9be6c20b6bb4 (PR #1599) touches the card OwnerFiles.
Доказательство: Commit 9be6c20b6bb4b333e154c585c20d5b63e627574c (PR #1599, fix(orchestrator): drop informational failures in parallel verdict (VC-06) (#1599)) touches OwnerFiles internal/app/orchestrator/checkall.go, internal/app/orchestrator/checkall_contract_test.go, internal/app/orchestrator/checkall_test.go; git merge-base --is-ancestor 9be6c20b6bb4b333e154c585c20d5b63e627574c origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1599 9be6c20b6bb4
Время закрытия: 2026-11-22T11:27:01Z
