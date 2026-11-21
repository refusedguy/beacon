# BCX-48. Запретить случайную регистрацию claim и lease инструментов
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_claim.go, docs/adrs/0263-mcp-boundary.md, tools/beacon-gates/gate/config/reachability.json
Зависимости: 
Ссылка: ADR 0263
Агент: exe22
Время: 2026-11-21T17:36:13Z
Цель: Убрать риск авто-регистрации семи готовых имён без решения владельца.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, таблица 4 строка 8. tool_claim.go:61-65 определяет beacon_claim и beacon_release, tool_claim.go:484-487 и :933 пять lease-имён. AttachClaim (:878) и AttachHolderLease (:1208) определены, не вызываются. go-metaprogramming.md:79 указывает, что авто-AddTool по файлам с обработчиками зарегистрировал бы их без решения. Базовый замер и приоритет из отчёта: P1.
Шаги: Добавить явный список разрешённых имён в точке AddTool и проверку на неизвестное имя. Отразить в ADR 0263, что claim и lease намеренно вне реестра. Обновить устаревшие причины в reachability.json.
Проверка: CI зелёный. Тест autoregistration не добавляет имя вне списка. Причины в reachability.json совпадают с ADR.
Приёмка: Ни один готовый Attach-хелпер не может widen контракт без правки списка и ADR.
