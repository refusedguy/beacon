# RS-CMP-02. Токен слота по сигналу завершения
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/app/orchestrator/decision.go, internal/app/orchestrator/decision_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать правило один сигнал равен один spawn машинным: выдавать одноразовый токен слота и гасить его только после проверки артефакта.
Контекст: Premise: проверено 2026-11-21, правило записано прозой в AGENTS.md и не проверяется кодом. baseline: `grep -rn 'slot\|Slot' internal/app/orchestrator/workload.go` пусто, а `grep -rn 'FixAttempts' internal/app/orchestrator/decision.go` находит счётчик попыток FIX. Источник: Claude Code agent teams, TeammateIdle с кодом выхода 2.
Шаги: Добавить тип токена слота в decision.go. Выдавать токен вместе с nextAction report_to_orchestrator. Погашать токен после проверки PLAN.md или PR. Считать несовпадение сигнала и спауна инцидентом. Тест с искусственно задержанным артефактом.
Проверка: gofmt чистый. Тесты в CI. Лишний слот не открывается при задержке артефакта.
Приёмка: Два сигнала от одной задачи не открывают два слота. Инцидент записывается в observe.
