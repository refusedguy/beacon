# IDX-02. Типизированный граф и запрет циклов
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/taskstore/edges.go, internal/infra/taskstore/edges_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Четыре типа рёбер blocks, depends-on, supersedes, related с отклонением циклов на записи.
Контекст: Premise: владелец запретил циклы. Сейчас whyEdgeRE понимает только T-numbers и не видит RS, WIRING, BS.
Шаги: Тип рёбра, проверка цикла при добавлении, обход с UNION и LIMIT, тесты на смешанные ID.
Проверка: gofmt, go test в CI.
Приёмка: Цикл не добавляется, все family ID попадают в граф.
