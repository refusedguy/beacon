# BS.67. Реестр исполнителей с capabilities
Статус: done
Фаза: P5
Тип: task
Владелец файлов: internal/infra/store/executors.go
Зависимости: BS.14
Ссылка: ADR 0191
Агент: нет
Время: нет
Цель: Недоступный исполнитель виден, но не выбирается.
Контекст: T3 capability discovery помечает unavailable без V2 adapter.
Шаги: capabilities, runtime, auth state, lease, heartbeat, список с флагом доступности.
Проверка: Запрос к недоступному исполнителю даёт типизированный отказ.
Приёмка: Ребёнок не расширяет права родителя.
