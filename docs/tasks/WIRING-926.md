# WIRING-926. Привязать CardLinks port в composition root
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
OwnerFiles: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
Зависимости: PR 926
Ссылка: PR 926
SourceWatch: watch:refusedguy/beacon:926:a8fc4525d9e213a0eae9df1d14aa1d0e2ea4195b
PR: 926
Агент: нет
Время: нет
Цель: production-композиция привязывает CardLinks port к cibridge, поэтому влитый Watch переводит связанную wiring-карточку в done.
Контекст: deferred-карточка PR 926. Причина отложенной проводки: порт WithCardLinks определён в cibridge, но production адаптер не привязан в cmd/beacon/mcp.go вне OwnerFiles IDX-08. Без привязки cibridge закрывает Watch, но не переводит отложенную карточку, и запись в `beacon deferred --format json` остаётся в todo.
Шаги: добавить адаптер deferredCardLinks в cmd/beacon/mcp.go, реализующий cibridge.CardLinks через ListDeferred и MarkDeferredDone, и вызвать WithCardLinks в ReconcilePass рядом с WithBlockerControl.
Проверка: gofmt без замечаний; при влитом Watch его отложенная запись переходит в done; повторное наблюдение влития не меняет карточку.
Приёмка: port привязан, CI зелёный, карточка и отложенная запись переведены в done.

## Причина

CardLinks port описан в cibridge как узкий интерфейс, чтобы пакет не импортировал store. Адаптер и его привязка живут в cmd/beacon/mcp.go, который не входил в OwnerFiles карточки IDX-08.

## Шаги

1. Реализовать адаптер deferredCardLinks в cmd/beacon/mcp.go поверх store.DB.
2. Привязать его через bridge.WithCardLinks(&deferredCardLinks{db: cfg.db}) в ReconcilePass.
3. Оставить матрицу слоёв без изменений.

## Проверка

- Связанная отложенная запись получает статус done после влития Watch.
- Повторный merged-observation идемпотентен.

## Приёмка

Проводка применена, CI зелёный, карточка и отложенная запись переведены в done.
