# RS2-OBS-01. Корреляционные поля в записи цикла
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/observe/observe.go, internal/infra/observe/log.go, internal/infra/observe/log_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Добавить watchID, obligationID, revision, role, adapter, verdict, attempt в cycleEvent и проверить связность по cycleID.
Контекст: Premise: RS-OBS-01 вернула блокер, cycleEvent живёт в bridge.go, константы в observe.go. Часть полей уже есть.
Шаги: Расширить cycleEvent, не дублировать существующие поля, тест связности, NO_CONTENT по умолчанию.
Проверка: gofmt, go test в CI.
Приёмка: По cycleID находится вся цепочка от Watch до квитанции.
