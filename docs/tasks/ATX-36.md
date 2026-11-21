# ATX-36. Помечать обрезанные failure excerpt и давать ссылку на лог
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/tool_get_result.go, internal/infra/mcp/schema.go
Зависимости: 
Ссылка: 
Агент: exe111
Время: 2026-11-21T17:57:39Z
Цель: Сделать потерю текста ошибки видимой для агента.
Контекст: Источник: docs/research/output-limits-audit.md, раздел 1 и строка таблицы 171. internal/infra/mcp/tool_get_result.go:68-74 задаёт failureExcerptLimit 2048 байт и failureExcerptAnnotationCount 8. sanitizeExcerpt в :453-489 режет по байтам с откатом до границы UTF-8 в :546-555. attachFailureExcerpts в :478 перестаёт добавлять проверки после 2048 байт. Continuation token нет, признака обрезания нет. SQLite кэш хранит уже урезанный текст. Схема в internal/infra/mcp/schema.go:129-133 обещает 2048 байт, но не имеет поля-признака. Baseline: 2048 байт и 8 аннотаций.
Шаги: Добавить failureExcerptTruncated и ссылку на полный лог в ответ. Обновить схему вывода. Сохранять в кэше признак обрезания.
Проверка: CI зелёный. Тест с длинной аннотацией видит признак обрезания и ссылку на источник. Тест с восемью аннотациями не теряет признак.
Приёмка: Агент видит, что текст ошибки неполный, и может прочитать полный лог.

Итог: Already landed in PR #1249 (commit 4406aea8833455690297927dc3299ddf220099af): truncated failure excerpts marked, full CI log linked in get_result response
Доказательство: PR #1249 merged; commit 4406aea8833455690297927dc3299ddf220099af changes internal/infra/mcp/tool_get_result.go and internal/infra/mcp/schema.go; git merge-base --is-ancestor 4406aea8833455690297927dc3299ddf220099af origin/main exit 0; no open duplicate PR
Причина: already landed in PR #1249 4406aea8
Время закрытия: 2026-11-21T18:05:58Z
