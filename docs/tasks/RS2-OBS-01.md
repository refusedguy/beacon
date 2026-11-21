# RS2-OBS-01. Корреляционные поля в записи цикла
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/observe/observe.go, internal/infra/observe/log.go, internal/infra/observe/log_test.go
Зависимости: 
Ссылка: PR #875
Агент: нет
Время: нет
Цель: Добавить watchID, obligationID, revision, role, adapter, verdict, attempt в cycleEvent и проверить связность по cycleID.
Контекст: Premise: RS-OBS-01 вернула блокер, cycleEvent живёт в bridge.go, константы в observe.go. Часть полей уже есть.
Шаги: Расширить cycleEvent, не дублировать существующие поля, тест связности, NO_CONTENT по умолчанию.
Проверка: gofmt, go test в CI.
Приёмка: По cycleID находится вся цепочка от Watch до квитанции.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #875 (https://github.com/refusedguy/beacon/pull/875, merge commit 3c99e77) влит. `grep -n 'FieldObligationID' internal/infra/observe/observe.go` = 49, `grep -n 'FieldAttempt' internal/infra/observe/observe.go` = 56, `grep -n 'func (b \*Bridge) cycleEvent' internal/infra/cibridge/bridge.go` = 447. cycleEvent ставит все семь полей на каждое событие цикла. Тест TestCycleCorrelationChainByCycleID возвращает цепочку по одному cycleID и исключает чужой cycleID. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
