# CCx-03. Ожидание готовности элемента перед dispatch
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/orchestrate.go, cmd/beacon/orchestrate_ready_test.go
Зависимости: CCx-01
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/parallel.rs:334-341, codex-rs/core/src/tools/multi_agent_tool.rs:121-123
Агент: ses_ccx03b_executor
Время: 2026-11-22T17:02:55Z
Цель: Не показывать элемент волны готовым, пока его рантайм не сообщил о готовности.
Контекст: Источник: docs/research/comp-codex.md. Codex вызывает `wait_until_ready` до захвата lock (`codex-rs/core/src/tools/parallel.rs:334-341`) и передаёт этот метод через обёртку инструмента (`codex-rs/core/src/tools/multi_agent_tool.rs:121-123`). Ожидание остаётся отменяемым. Beacon admission в `cmd/beacon/orchestrate.go:589-623` не знает о готовности рантайма и может принять элемент, сервис которого ещё недоступен. Baseline: 0 вызовов ожидания готовности в пути волны.
Шаги: Добавить в описание элемента функцию готовности, которая может отсутствовать. Вызывать её до захвата слота под отмену. Не запускать элемент, пока готовность не получена. Покрыть тестом с одним задержанным и одним готовым элементом.
Проверка: CI зелёный. Тест показывает 0 запусков задержанного элемента до сигнала готовности и 1 запуск готового элемента в тот же период.
Приёмка: Изменения только в OwnerFiles карточки. Элементы без функции готовности ведут себя как сегодня.

Причина: Already landed in PR #1723 (commit 7c48dccd9a458dd1eeceb8b15e1bc0e3c20f134b). git merge-base --is-ancestor 7c48dccd9a458dd1eeceb8b15e1bc0e3c20f134b origin/main exit 0. Owner files cmd/beacon/orchestrate.go and cmd/beacon/orchestrate_ready_test.go already carry the readiness-wait fix.
Закрыл: ses_ccx03b_executor
Время закрытия: 2026-11-22T17:03:27Z
