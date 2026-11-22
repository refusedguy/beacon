# BCX-49. Устранить конфликт wake-конструкторов с ADR 0263
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_wake.go, tools/beacon-gates/gate/config/reachability.json, docs/adrs/0263-mcp-boundary.md
Зависимости: 
Ссылка: ADR 0263
Агент: нет
Время: нет
Цель: Привести пять wake-конструкторов и причины их отложки к одному решению.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, таблица 4 строка 9. AttachWake в tool_wake.go:105-107, :1740-1747 определён, не вызывается. 25 вызовов только в тестах по deadcode-audit.md:30. NewCapabilityFallbackWakeHandler и ещё четыре конструктора вынесены в REACH-UNWIRED (reachability.json:75-120) с причиной про Waves 6-8, что противоречит ADR 0263. Базовый замер и приоритет из отчёта: P1.
Шаги: Выбрать: удалить мёртвые конструкторы или оставить с причиной, которая ссылается на ADR 0263 и не обещает подключение в будущих волнах. Обновить reachability.json. Добавить проверку, что причина REACH-UNWIRED не противоречит принятому ADR.
Проверка: CI зелёный. Причины в reachability.json не обещают подключение, запрещённое ADR. Тест на удалённые или помеченные конструкторы проходит.
Приёмка: Конфиг отложенных символов и ADR говорят одно и то же о wake.

Итог: Already landed in PR #1302 (commit 5dc4e1e8d2dbcc6c8d388f041cb72d85451f6522). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1302 merged 2026-11-22T09:42:40+05:00; commit 5dc4e1e8d2dbcc6c8d388f041cb72d85451f6522; git merge-base --is-ancestor 5dc4e1e8d2dbcc6c8d388f041cb72d85451f6522 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1302 5dc4e1e8
Время закрытия: 2026-11-22T11:24:28Z
