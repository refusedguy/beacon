# BS.28. Signature, filters и dedupe вебхука
Статус: done
Обоснование: функциональность уже в main. PR #461 (commit c3ca04b) добавил internal/infra/webhook/filters.go ровно в OwnerFiles карточки. Filter фильтрует по событию, ветке и автору через FilterConfig, дедуплицирует по delivery ID и head SHA, возвращает (Decision, reason) для аудита; Decision.Publish() истинно только на DecisionAccept, поэтому отфильтрованное событие не порождает outbox. Дубля в OwnerFiles нет, код не писался.
Фаза: P2
Тип: task
Владелец файлов: internal/infra/webhook/filters.go
Зависимости: BS.27
Ссылка: ADR 0193
Агент: нет
Время: нет
Цель: Отсечь нерелевантные запросы до постановки в очередь.
Контекст: T3 упоминает signature, filters и dedupe как часть scheduler.
Шаги: Фильтры по событию, ветке, автору; дедупликация по delivery и head SHA.
Проверка: Фильтрованное событие не порождает outbox.
Приёмка: Решение фильтра пишется в аудит.

## Подтверждение
- Premise проверен 2026-11-20, дубль не создан. Функциональность уже в main: PR #461 (commit c3ca04b) добавил `internal/infra/webhook/filters.go` ровно в OwnerFiles карточки. `Filter` фильтрует по событию, ветке и автору через `FilterConfig`, дедуплицирует по delivery ID и head SHA, ведёт индекс под мьютексом и возвращает `(Decision, reason)` для аудита. `Decision.Publish()` даёт true только на `DecisionAccept`, поэтому отфильтрованное событие не порождает outbox. Чистое ядро `decide` отделено от состояния. `filters_test.go` покрывает отказ по каждому полю, оба вида дедупликации, отказ публиковать и сходимость индекса под параллельными вызовами (`TestFilteredDecisionDoesNotPublish`, `TestFilterDedupeConvergesUnderConcurrency`). Новый код в OwnerFiles создал бы вторую копию механизма.
