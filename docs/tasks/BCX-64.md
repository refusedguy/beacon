# BCX-64. Определить статус wake вне MCP и сделать проверку ADR 0026 достижимой
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_wake.go, docs/adrs/0026-honest-followup-delivery.md, docs/adrs/0059-six-features-from-aq-wave.md, docs/adrs/0263-mcp-boundary.md
Зависимости: 
Ссылка: ADR 0026
Агент: нет
Время: нет
Цель: Убрать ситуацию, когда принятый ADR требует живую проверку недоступного инструмента.
Контекст: Источник: docs/research/adr-consistency-audit.md, таблица строка 3. tool_wake.go содержит Delivery*, NewHeartbeatGuard, ToRepairableErrorShape, но NewServer не вызывает AttachWake. ADR 0263 запрещает регистрацию. ADR 0026 требует обязательное delivery и живой прогон с агентом, вернувшим queued. Живая проверка недостижима на main. Базовый замер и приоритет из отчёта: P1.
Шаги: Выбрать: зарегистрировать wake и выполнить живой тест ADR 0026, либо перевести ADR 0026 и 0059 в состояние с явной оговоркой, что доставка живёт вне MCP и проверяется другим путём. Обновить карточки, которые ссылаются на живую проверку.
Проверка: CI зелёный. ADR описывает достижимый способ проверки. Тест, предписанный ADR, запускается в CI или помечен как заменённый.
Приёмка: ADR 0026 не требует недостижимую проверку. Статус доставки честно отражён в документации.

Итог: Already landed in PR #1331 (commit b6cefb28ea6c3c0506a165520525bd0cc048525b). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1331 merged 2026-11-21T22:53:18+05:00; commit b6cefb28ea6c3c0506a165520525bd0cc048525b; git merge-base --is-ancestor b6cefb28ea6c3c0506a165520525bd0cc048525b origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1331 b6cefb28
Время закрытия: 2026-11-22T11:24:28Z
