# RS2-OBS-03. Метка исхода доставки в метриках
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/observe/metrics.go, internal/infra/observe/observe.go, internal/infra/cibridge/bridge.go, cmd/beacon/deliver.go, internal/infra/observe/metrics_test.go
Зависимости: 
Ссылка: PR #876
Агент: нет
Время: нет
Цель: Пробросить исход доставки в WakeDeliveredSource и WakeFailed, добавить счётчик по adaptation и outcome.
Контекст: Premise: RS-OBS-03 вернула блокер, хелперы в observe.go:444-515 не принимают outcome, он вычисляется в bridge.go и deliver.go.
Шаги: Пробросить параметр, добавить метку, тесты на transport-failed и duplicate, сохранить совместимость JSON.
Проверка: gofmt, go test в CI.
Приёмка: Метрика отвечает, какой адаптер чаще даёт transport-failed.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #876 (https://github.com/refusedguy/beacon/pull/876, merge commit fef00fd) влит. `grep -n 'CounterWakeOutcome' internal/infra/observe/metrics.go` = 55, `grep -n 'WakeOutcome(' internal/infra/observe/observe.go` = 549, `grep -n 'WakeDeliveredOutcome' internal/infra/observe/observe.go` = 572. Счётчик wake_outcome_total несёт составную метку adapter:outcome. Вызовы в bridge.go и CLI передают transport-failed и duplicate, формат JSON старых счётчиков не изменён. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
