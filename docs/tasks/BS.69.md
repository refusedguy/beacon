# BS.69. Lineage дочерних thread
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/app/session/delegation.go
Зависимости: BS.66
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: Один чат управляет всеми детьми.
Контекст: T3 child thread имеет lineage subagent и проекцию родителю.
Шаги: parent_id, lineage, transfer контекста, уведомление родителю при вопросе.
Проверка: Вопрос ребёнка появляется в родительском чате с именем ребёнка.
Приёмка: Ссылки на thread создаёт сервер.

## Подтверждение
- PR #394 DROPPED: Child-thread lineage/context transfer from old T3 architecture; no child threads in the 7-tool/ses_ contract.
