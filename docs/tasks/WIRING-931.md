# WIRING-931. Подключить beacon index к dispatch CLI
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/main.go, cmd/beacon/catalog.go, cmd/beacon/main_test.go, cmd/beacon/catalog_test.go
OwnerFiles: cmd/beacon/main.go, cmd/beacon/catalog.go, cmd/beacon/main_test.go, cmd/beacon/catalog_test.go
Зависимости: PR 931
Ссылка: PR 931
SourceWatch: watch:refusedguy/beacon:931:3c931161190b196eb97875e813be90b8a88c9fda
PR: 931
Агент: нет
Время: нет
Цель: beacon index build доступен из dispatch через RegisterIndex и присутствует в каталоге команд.
Контекст: deferred-карточка PR 931. Причина отложенной проводки: IDX-03 владеет cmd/beacon/index.go и .githooks/pre-commit. Case "index" в cmd/beacon/main.go и запись в каталог команд лежат вне OwnerFiles этой карточки, поэтому RegisterIndex остаётся без вызова из dispatch. Файл cmd/beacon/index.go в этот PR не входит.
Шаги: добавить case "index" в dispatch, вызывающий indexRunner, установить RegisterIndex(runIndex) в composition root, добавить запись index в catalog и ожидаемое имя в тест каталога, обновить usage.
Проверка: `beacon list --format json` содержит index, `beacon index build --root <dir>` пересобирает индекс и печатает счётчики, gofmt без замечаний.
Приёмка: dispatch-ветка, регистрация и каталог на месте, CI зелёный, карточка и отложенная запись переведены в done.

## Причина

Команда index и её runner добавлены PR 931, но dispatch и каталог команд находятся в cmd/beacon/main.go и cmd/beacon/catalog.go, которые не входят в OwnerFiles IDX-03.

## Шаги

1. Добавить case "index" в cmd/beacon/main.go, вызывающий indexRunner.
2. Установить RegisterIndex(runIndex) в init() в cmd/beacon/main.go.
3. Добавить запись index в cmd/beacon/catalog.go и обновить cmd/beacon/main_test.go.
4. Не трогать cmd/beacon/index.go и матрицу слоёв.

## Проверка

- `beacon index build` доступен из CLI.
- Каталог и тест каталога согласованы.

## Приёмка

Проводка применена, CI зелёный, карточка и отложенная запись переведены в done.

## Подтверждение

PR #951 (commit 6e4b918) влит в origin/main 2026-11-21. Он добавил case "index" в dispatch, вызов RegisterIndex(runIndex) в init(), запись index в cmd/beacon/catalog.go и имя index в TestT06ListJSONCatalog. Заголовок PR явно называет WIRING-931. Код лежит в origin/main, OwnerFiles карточки не менял.
