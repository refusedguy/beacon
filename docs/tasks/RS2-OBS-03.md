# RS2-OBS-03. Метка исхода доставки в метриках
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/observe/metrics.go, internal/infra/observe/observe.go, internal/infra/cibridge/bridge.go, cmd/beacon/deliver.go, internal/infra/observe/metrics_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Пробросить исход доставки в WakeDeliveredSource и WakeFailed, добавить счётчик по adaptation и outcome.
Контекст: Premise: RS-OBS-03 вернула блокер, хелперы в observe.go:444-515 не принимают outcome, он вычисляется в bridge.go и deliver.go.
Шаги: Пробросить параметр, добавить метку, тесты на transport-failed и duplicate, сохранить совместимость JSON.
Проверка: gofmt, go test в CI.
Приёмка: Метрика отвечает, какой адаптер чаще даёт transport-failed.
