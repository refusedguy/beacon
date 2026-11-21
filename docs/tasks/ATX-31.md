# ATX-31. Применять MaxRebases и делать попытку ребаза durable
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_watch_pr.go, internal/infra/cibridge/bridge.go
Зависимости: 
Ссылка: 
Агент: exe106
Время: 2026-11-21T17:56:22Z
Цель: Не давать Watch зацикливаться на ребазе после рестарта процесса.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 5 и строка таблицы 117. internal/infra/mcp/tool_watch_pr.go:446-455 пишет MaxRebases в Watch, но ни один продакшен-путь его не читает. ADR 0259:15 подтверждает, что поле пока не управляет блокировкой. claimRebase в bridge.go:379-401 даёт одну попытку на пару (watchID, revision) только в памяти. После рестарта пометка забывается.
Шаги: Списать ребаз в обязательство Watch по ключу (watchID, revision) и сравнивать с MaxRebases перед выдачей verdict rebase. При исчерпании ставить blocked в общий инцидент.
Проверка: CI зелёный. Тест с MaxRebases = 1 и двумя неудачными ребазами даёт blocked. Тест с рестартом процесса сохраняет счётчик.
Приёмка: Бюджет ребазов читается в продакшене. Попытка переживает рестарт.
Итог: Требование уже закрыто коммитом 921e8f043c54e021fab4eff53ec88debe1e6551c в PR #1255, отдельный код-PR не нужен.
Доказательство: git merge-base --is-ancestor 921e8f043c54e021fab4eff53ec88debe1e6551c origin/main -> exit 0; PR #1255; github.com/refusedguy/beacon/pull/1255; изменения в internal/infra/mcp/tool_watch_pr.go и internal/infra/cibridge/bridge.go.
