# BS.1. Single-instance lease и pidfile
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/server.go, internal/infra/server/lifecycle.go
Зависимости: 
Ссылка: ADR 0188
Агент: нет
Время: нет
Цель: Один beacon-server на машину.
Контекст: Два сервера откроют одну БД и удвоят wake.
Шаги: Блокировка файла в state dir, pidfile, проверка living process, отказ при втором старте.
Проверка: Два параллельных старта: второй завершается с ясной ошибкой.
Приёмка: lease снимается при обычном и аварийном выходе.
