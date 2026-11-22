# INTG-01. Зарегистрировать serverRunner в диспетчере команд
Статус: done
Premise: PR #1450, коммит 851d533e4f3240d64445a596e3d25997af43024f, зарегистрировал сервер в dispatch и каталоге. git merge-base --is-ancestor 851d533e4f3240d64445a596e3d25997af43024f origin/main вернул 0.
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/server_test.go
Зависимости: 
Ссылка: ADR 0016
Агент: нет
Время: нет
Цель: Сделать команду beacon server доступной через общий диспетчер runnerFor.
Контекст: Источник: docs/research/audit-05-integrity.md, раздел "Новое, что не вошло в AUD", пункт 1. cmd/beacon/server.go:130 определяет serverRunner, но в cmd/beacon/main.go:243-277 таблица runnerFor не содержит ключа server, а cmd/beacon/catalog.go:32-54 не содержит записи server. Отдельного бинарника beacon-server в репозитории нет. Команда beacon server падает как неизвестная. AUD-04 синхронизирует только уже существующие ключи и этот случай не покрывает. baseline: 0 registrations of serverRunner in runnerFor.
Шаги: Добавить ключ server в runnerFor с адаптером signature func(io.Writer) int к commandRunner. Добавить тест в cmd/beacon/server_test.go, который вызывает dispatch с аргументом server и проверяет вызов serverRunner через подмену мcpRunner. Добавить проверку в cmd/beacon/main_test.go, что runnerFor[server] не nil. Запись в catalog оставить владельцу AUD-04, который синхронизирует каталог с диспетчером.
Проверка: CI зелёный. beacon server вызывает serverRunner и не возвращает unknown command. Тест в server_test.go проходит с подменённым mcpRunner.
Приёмка: Изменения только в OwnerFiles карточки. Ключ server присутствует в runnerFor. Повторный запуск команды не меняет другие dispatch-пути.
