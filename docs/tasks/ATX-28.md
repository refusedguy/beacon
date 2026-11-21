# ATX-28. Считать FIX-попытки в Watch и включать escalate_human
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/app/orchestrator/decision.go, internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/store.go, internal/infra/store/blockers.go, cmd/beacon/orchestrate.go
Зависимости: 
Ссылка: 
Агент: exe103
Время: 2026-11-21T17:55:57Z
Цель: Замкнуть цикл FIX-агентов и дойти до escalate_human после трёх попыток.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 2 и строка таблицы 114. internal/app/orchestrator/decision.go:226-236 сравнивает FixAttempts с MaxFixAttempts = 3. handleBlocked в orchestrator.go:399-419 намеренно не пишет счётчик. RecordFixAttempt объявлен в internal/app/orchestrator/store.go:107, но вызовов вне тестов нет. AssignFixer в internal/infra/store/blockers.go:209-231 увеличивает счётчик инцидента, а не Watch.BlockedInfo. projectBlocked в cmd/beacon/orchestrate.go:139-149 читает Watch. ADR 0260 требует границы цикла.
Шаги: Вызывать RecordFixAttempt при подтверждённом спавне фиксера или читать счётчик инцидента в projectBlocked. Оставить один источник истины. Добавить тест на переход к ReasonEscalateHuman на третьей попытке.
Проверка: CI зелёный. Тест с тремя подтверждёнными FIX-спавнами даёт escalate_human. Повторный тик демона не списывает попытку.
Приёмка: Счётчик растёт только по факту спавна. Бесконечный BLOCKED_FIX_AGENT исключён.
