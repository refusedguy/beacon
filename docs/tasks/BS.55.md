# BS.55. Синхронизация backlog
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/app/tasks/backlog_sync.go
Зависимости: BS.53, BS.22
Ссылка: ADR 0203
Агент: нет
Время: нет
Цель: Issue и карточки не расходятся.
Контекст: Ночная смена требует триажа и сверки статусов.
Шаги: Чтение issue, сверка с карточками, создание карточек через batch-apply.
Проверка: Повтор синхронизации не создаёт дубликаты.
Приёмка: Конфликт статусов пишется в аудит.

## Подтверждение
- PR #394 DROPPED: Automated creation of cards via batch-apply contradicts AGENTS.md ('batch-apply без команды владельца'); issue triage is covered by .opencode/skills/night-shift.
