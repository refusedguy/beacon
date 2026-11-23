# CCx-02. Повторная проверка бюджета и deny после ожидания слота
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/orchestrate.go, cmd/beacon/orchestrate_budget_test.go
Зависимости: CCx-01
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/parallel.rs:328-332, :361-370
Агент: ses_ccx02b_executor
Время: 2026-11-22T16:58:38Z
Цель: Не запускать элемент волны, если бюджет исчерпан или deny появился во время ожидания слота.
Контекст: Источник: docs/research/comp-codex.md. Codex проверяет исчерпание бюджета до dispatch (`codex-rs/core/src/tools/parallel.rs:328-332`). После ожидания слота он снова читает `tool_start_denial` и отменяет вызов (`parallel.rs:361-370`). Beacon принимает все элементы один раз в `planWave` (`cmd/beacon/orchestrate.go:589-623`) и не перечитывает состояние перед запуском. У плана нет поля бюджета и нет deny-предиката. Baseline: 0 проверок бюджета и 0 повторных проверок deny в `cmd/beacon/orchestrate.go:584-624`.
Шаги: Добавить предикат бюджета и предикат deny в операцию admission. Проверять их до захвата слота и сразу после ожидания. Возвращать причину отказа вместе с решением плана. Покрыть двумя тестами: бюджет исчерпан другим элементом, deny выставлен во время ожидания.
Проверка: CI зелёный. Первый тест показывает 1 отказ и 0 запущенных элементов второго вызова. Второй тест показывает 0 запусков при deny, который появился после ожидания слота.
Приёмка: Изменения только в OwnerFiles карточки. Текущее поведение `planWave` для крупных задач не ломать.

Причина: Already landed in origin/main via PR #1725, commit 73cb145e2803fdc25a7646f950538dcb3d933ac8 (git merge-base --is-ancestor origin/main exit 0); touches only OwnerFiles cmd/beacon/orchestrate.go and cmd/beacon/orchestrate_budget_test.go. No duplicate code needed.
Закрыл: ses_ccx02b_executor
Время закрытия: 2026-11-22T16:58:55Z
## Подтверждение
- PR #1979 MERGED: 2026-11-22 (squash).

