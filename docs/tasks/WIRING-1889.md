# WIRING-1889. Wire PrepareWakeOutput into beacon_get_result output
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/mcp/tool_get_result.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1889
Ссылка: PR 1889
SourceWatch: watch:refusedguy/beacon:1889:cfe313ca92391c055b13866fc96eecba281cbb39@1795362609:1800
PR: 1889
Агент: ses_wiring1889_executor
Время: 2026-11-22T18:26:56Z
Цель: Подключить PrepareWakeOutput к заполнению FailureExcerpt в beacon_get_result.
Контекст: CA-24 добавил измерение вывода и порог подтверждения в internal/infra/github/rest.go. Точка вставки вывода в wake находится в internal/infra/mcp/tool_get_result.go, вне OwnerFiles карточки CA-24.
Шаги: Дождаться мержа PR 1889; вызвать PrepareWakeOutput перед заполнением FailureExcerpt; показать число строк и токенов в отчёте; сохранить автоматический фрагмент для упавших проверок; вызвать `beacon deferred done` с watchID из SourceWatch.
Проверка: Малый вывод вставляется без запроса, большой без подтверждения не вставляется, упавшая проверка даёт фрагмент не более 2048 байт, вердикты не меняются.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

Причина: already_landed
Закрыл: beacon
Время закрытия: 2026-11-22T18:34:36Z
## Причина

Точка вставки вывода в wake лежит в internal/infra/mcp/tool_get_result.go. Этот файл не входит в OwnerFiles карточки CA-24, поэтому вызов PrepareWakeOutput вынесен в отдельную проводку.

<!-- wiring-card:contract
beacon_defer_wiring записал проводку в durable store. Карточка добавлена в тот же PR.
Статус todo, OwnerFiles заполнен, раздел "## Причина" есть.
wiring-card:contract -->

## Подтверждение
- PR #2150 MERGED: 2026-11-22 (squash).
