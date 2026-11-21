# RS-CMP-02. Токен слота по сигналу завершения
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/app/orchestrator/decision.go, internal/app/orchestrator/decision_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
baseline: `grep -rn 'slot\|Slot' internal/app/orchestrator/workload.go` = 0; `grep -rn 'FixAttempts' internal/app/orchestrator/decision.go` = 6 строк (39, 42, 79, 85, 212, 229); `grep -rn 'report_to_orchestrator' internal/` = internal/infra/cibridge/bridge.go:1010 и три теста; `grep -rn 'SlotToken\|ArtifactVerifier\|SlotLedger' internal/app/orchestrator/` до правки = 0. Правило «один сигнал = один spawn» жило только в AGENTS.md и кодом не проверялось.
Решение: В decision.go добавлены типы SlotToken, SlotArtifact, SlotIncident и SlotLedger. Метод Issue выдаёт одноразовый токен только на сигнал report_to_orchestrator. Ключ токена состоит из watchID и revision. Повторный сигнал с тем же ключом возвращает инцидент duplicate_signal и не открывает второй слот. Метод Burn гасит токен только после того, как ArtifactVerifier подтвердил артефакт PLAN.md или PR. Задержка артефакта или ошибка проверки оставляют слот занятым. Burn без живого токена возвращает инцидент spawn_without_token. Тест TestSlotLedgerDelayedArtifact задерживает артефакт и проверяет, что второй сигнал во время задержки не открывает второй слот.
Граница: Модель чистая. Она не читает файлы, не вызывает GitHub и не доставляет wake. Запись инцидента в observe и доставка wake остаются за пределами OwnerFiles этой карточки.
Блокер: Механизацию нельзя довести до конца в этих OwnerFiles. Проверка PR живёт в internal/app/orchestrator/daemon.go (порт Verify, строки 47 и 112) и internal/app/orchestrator/store.go (MergedPRReader, строка 255). Чтения PLAN.md как артефакта в коде нет. Сигнал report_to_orchestrator издаётся в internal/infra/cibridge/bridge.go:1010. Wake доставляется в internal/app/orchestrator/orchestrator.go (wakeOne, строка 550) и инструментами internal/infra/mcp/server.go. Запись инцидента живёт в internal/infra/observe/observe.go и internal/infra/observe/metrics.go. Все перечисленные файлы вне OwnerFiles и не тронуты. Для подключения модели нужен владелец этих файлов и отдельная карточка.
Цель: Сделать правило один сигнал равен один spawn машинным: выдавать одноразовый токен слота и гасить его только после проверки артефакта.
Контекст: Premise: проверено 2026-11-21, правило записано прозой в AGENTS.md и не проверяется кодом. baseline: `grep -rn 'slot\|Slot' internal/app/orchestrator/workload.go` пусто, а `grep -rn 'FixAttempts' internal/app/orchestrator/decision.go` находит счётчик попыток FIX. Источник: Claude Code agent teams, TeammateIdle с кодом выхода 2.
Шаги: Добавить тип токена слота в decision.go. Выдавать токен вместе с nextAction report_to_orchestrator. Погашать токен после проверки PLAN.md или PR. Считать несовпадение сигнала и спауна инцидентом. Тест с искусственно задержанным артефактом.
Проверка: gofmt чистый. Тесты в CI. Лишний слот не открывается при задержке артефакта.
Приёмка: Два сигнала от одной задачи не открывают два слота. Инцидент возвращается значением SlotIncident и должен попасть в observe после подключения файлов из поля Блокер.
