# IDX-02. Типизированный граф и запрет циклов
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/taskstore/edges.go, internal/infra/taskstore/edges_test.go
Зависимости: 
Ссылка: ADR 0066
Агент: ses_e02a360ebffezBR5X1jcmC2KN5
Время: 2026-11-21T09:14:17Z
Цель: Четыре типа рёбер blocks, depends-on, supersedes, related с отклонением циклов на записи.
Контекст: Premise: владелец запретил циклы. Проверка 2026-11-21 показала, что whyEdgeRE уже видит все семейства ID через whyCardID в cmd/beacon/task.go. Этот файл не входит в OwnerFiles и не меняется. Разрыв остался в taskstore: типизированного графа там нет.
baseline: `git grep -n -E 'EdgeKind|AddEdge|type Graph' origin/main -- internal/infra/taskstore/` пусто. `grep -n whyCardID cmd/beacon/task.go` даёт строку 87 с регуляркой на все семейства. Источник: репозиторий refusedguy/beacon на коммите f032156.
Шаги: Тип рёбра, проверка цикла при добавлении, обход с UNION и LIMIT, тесты на смешанные ID.
Проверка: gofmt, go test в CI.
Приёмка: Цикл не добавляется, все family ID попадают в граф.
Измерение: До правки в internal/infra/taskstore не было типов рёбер, проверки цикла и обхода. Поиск по EdgeKind, AddEdge и type Graph дал пустой результат. После правки AddEdge отклоняет петлю и ребро, замыкающее цикл, с кодом 1. IsID пропускает T3.17, S1.001, BS.1, RS-MCP-02, WIRING-485 и UI-31. Решение принято по этому замеру.
Решение: Добавлены EdgeKind и Graph в edges.go. AddEdge проверяет тип, ID и достижимость начала из конца перед записью. Traverse объединяет найденные узлы в множество посещённых и ограничивает их число через LIMIT. Повторное ребро не меняет граф. Обход и проверка цикла работают на прямых рёбрах из карты по узлу.

## Подтверждение
- Ветка fix/z2 от origin/main (f032156). Изменены только свои OwnerFiles и эта карточка.
- gofmt -w на двух файлах выполнен локально. go test и go build локально не запускались по правилам репозитория, проверка идёт в CI PR.
