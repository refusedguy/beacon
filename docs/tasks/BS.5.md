# BS.5. SQLite WAL, один writer, read pool
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/store/db.go
Зависимости: 
Ссылка: ADR 0188
Агент: нет
Время: нет
Цель: Один writer и пул читающих соединений.
Контекст: openrig открывает SQLite в WAL с foreign keys и миграциями при старте.
Шаги: PRAGMA journal_mode=WAL, foreign_keys=ON, migrate, отдельный writer mutex.
Проверка: Параллельные чтения не блокируют writer.
Приёмка: Схема имеет version и путь миграции из старого состояния.
