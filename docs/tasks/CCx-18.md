# CCx-18. Синхронизация тестов через сигналы, а не sleep
Статус: done
Фаза: P2
Тип: task
Владелец файлов: tests/e2e/e2e_test.go, internal/infra/observe/observability_test.go, tests/e2e/sync_signals_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/code-mode-host/AGENTS.md:3
Агент: ses_ccx18b_executor
Время: 2026-11-22T18:20:24Z
Цель: Заменить фиксированные задержки в тестах на ожидание наблюдаемого состояния.
Контекст: Источник: docs/research/comp-codex.md. Правило Codex запрещает sync по timeouts и sleeps (`codex-rs/code-mode-host/AGENTS.md:3`). Тест ждёт явный сигнал или наблюдаемое состояние, а предел времени задаёт runner. Beacon использует фиксированные задержки в e2e тестах: 500 мс в `tests/e2e/e2e_test.go:693` и `:724`, 250 мс в `:615`, 1 мс в `internal/infra/observe/observability_test.go:502`. Такая задержка не доказывает, что событие доставлено. Baseline: 8 вызовов `time.Sleep` в e2e тестах и 1 в observability-тесте.
Шаги: Перечислить события, которых ждут задержки. Заменить каждую задержку циклом с предикатом observable state и дедлайтом runner. Оставить таймаут только как предел тест-кейса. Покрыть проверкой, что список вызовов `time.Sleep` в названных файлах пуст.
Проверка: CI зелёный. `rg 'time.Sleep' tests/e2e/e2e_test.go internal/infra/observe/observability_test.go` возвращает 0 строк. Тест на медленном runner проходит без увеличения задержек.
Приёмка: Изменения только в OwnerFiles карточки. Порядок проверок в CI не менять.

Причина: Уже влито в main через PR #1832, коммит 02bc6b7914bc58672b3f09485ebbf0488b817f2a. git merge-base --is-ancestor 02bc6b7914bc58672b3f09485ebbf0488b817f2a origin/main -> exit 0. Дубль кода не создан.
Закрыл: ses_ccx18b_executor
Время закрытия: 2026-11-22T18:21:14Z