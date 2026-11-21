# RS-CMP-05. Классификация восстановления снапшота
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/store/snapshots.go, internal/infra/store/snapshots_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: При восстановлении топологии возвращать для каждой ноды явный исход resumed, fresh-primed, awaiting-decision, attention-required или failed.
Контекст: Premise: проверено 2026-11-21, snapshots.go существует, но явной классификации исходов нет. baseline: `grep -rn 'resumed\|fresh-primed\|awaiting-decision' internal/infra/store/snapshots.go` пусто. Источник: OpenRig rig up возвращает явный исход для каждой ноды.
Шаги: Добавить перечисление исходов и функцию классификации по состоянию сессии и Watch. Не трогать формат самого снапшота без нужды. Тесты на все пять исходов и на отсутствие сессии.
Проверка: gofmt чистый. Тесты в CI. Старые снапшоты читаются.
Приёмка: Восстановление больше не оставляет ноду в неопределённом состоянии.

## Подтверждение
- Baseline проверен 2026-11-21: `grep -rn 'resumed\|fresh-primed\|awaiting-decision' internal/infra/store/snapshots.go` пусто, классификации нет ни в рабочем дереве, ни в origin/main. Дубля нет.
- В `internal/infra/store/snapshots.go` добавлены `RecoveryOutcome` с пятью явными значениями (`resumed`, `fresh-primed`, `awaiting-decision`, `attention-required`, `failed`), вход `RecoveryNode` и чистая функция `ClassifyRecovery`. Формат снапшота не менялся.
- Правила в приоритетном порядке: ошибка или отсутствие Watch даёт `failed` и не подменяется свежим запуском (ADR 0113); терминальный Watch, заблокированная Watch, verdict из набора rebase/retry/keep_working/timeout/blocked и закрытая сессия дают `attention-required`; `ready` даёт `awaiting-decision`; здоровая Watch без сессии даёт `fresh-primed`; активная сессия со здоровой Watch даёт `resumed`.
- В `internal/infra/store/snapshots_test.go` добавлены тесты на все пять исходов, отдельный тест на отсутствие сессии и тест, что `failed` не превращается в `fresh-primed`.
- OwnerFiles карточки: `internal/infra/store/snapshots.go`, `internal/infra/store/snapshots_test.go`. Другие файлы, `wiring/` и `.beacon/` не тронуты.
- Проверено локально: `gofmt -l` по обоим файлам пусто, `go build ./internal/infra/store/...` проходит. `go test` локально не запускался, тесты идут в CI по проверке карточки.
