# CCx-16. Версия схемы trace и связность writer, reader, fixture
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/atomic_write.go, internal/infra/store/schema_contract_test.go, docs/evals/ccx16-trace-schema.md
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/rollout-trace/AGENTS.md:7-10, :11-16
Агент: ses_ccx16c_executor
Время: 2026-11-22T18:16:35Z
Цель: Не давать формату события измениться без bumps версии и обновления всех читателей и фикстур.
Контекст: Источник: docs/research/comp-codex.md. Правило Codex требует менять writer, reducer и фикстуры вместе (`codex-rs/rollout-trace/AGENTS.md:7-10`). Каждое изменение схемы `state.json` поднимает `REDUCED_TRACE_SCHEMA_VERSION` (`:11-16`). Beacon хранит `defaultWakeSchemaVersion = 2` как константу (`internal/infra/store/atomic_write.go:26-30`). Golden-схема MCP закреплена комментарием (`internal/infra/mcp/schema.go:268-270`). Автоматической связи между структурой события, версией и фикстурой нет. Baseline: 1 hard-coded версия, 0 проверок связности writer и reader.
Шаги: Вынести версию схемы события в тип с явным правилом bumps. Добавить тест, который сверяет payload writer, decoder и golden fixture. Отдельно проверить, что raw-изменение без изменения consumer API не поднимает версию. Обновить eval с таблицей решений.
Проверка: CI зелёный. Тест с новым полем payload без bumps версии падает. После bumps версии и обновления фикстуры тест проходит.
Приёмка: Изменения только в OwnerFiles карточки. Текущая версия 2 и формат journal row сохранить.

Причина: Уже влито в main через PR #1816, полный SHA f7bae2f438367c1c34be2c3abc20404d93e57457; git merge-base --is-ancestor f7bae2f438367c1c34be2c3abc20404d93e57457 origin/main = 0. Коммит изменил все три OwnerFiles: internal/infra/store/atomic_write.go, internal/infra/store/schema_contract_test.go, docs/evals/ccx16-trace-schema.md. Дубли кода не делаем.
Закрыл: ses_ccx16c_executor
Время закрытия: 2026-11-22T18:17:28Z
## Подтверждение
- PR #2112 MERGED: 2026-11-22 (squash).

