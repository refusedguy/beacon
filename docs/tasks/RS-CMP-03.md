# RS-CMP-03. Явные ступени доставки wake
Premise: проверено 2026-11-21. Модель обязательства `domain.Obligation` определена в `internal/domain/types.go:295`, вне OwnerFiles карточки. Шаг карточки требует добавить поля `deliveryRung`, `deliveryAttempts` и `nextRetryAt` в обязательство. Поля нельзя добавить к структуре другого пакета без правки её файла. `saveObligation` в `internal/infra/ladder/transitions.go:301` маршалит ровно `domain.Obligation`, поэтому поля на обёртке в пакете ladder теряются при записи и чтении. Повторы по revoke-событию требуют нового события: `Event` в `internal/domain/transition.go:98` описывает verdict, cancel, head_changed и gate-события, ревока в нём нет. Приёмка "лестница доставки видна в beacon inspect" требует правки `cmd/beacon/inspect.go`: команда читает вотчи, блокеры, оркестратор, статистику и ESCALATIONS.md, но не обязательства и не маркеры лестницы. Карточке нужны три файла вне OwnerFiles: `internal/domain/types.go`, `internal/domain/transition.go`, `cmd/beacon/inspect.go`. Без них реализация в OwnerFiles невозможна. Wake cap в `internal/infra/ladder/transitions.go:38` не менял. Статус todo сохранён, блокер возвращён оркестратору с именем файла модели обязательства `internal/domain/types.go`. Код не писал, gofmt не применял, OwnerFiles не менял, дубль не создал.
baseline: `grep -rn 'deliveryRung\|nextRetryAt' internal/infra/ladder | wc -l` = 0; `grep -n 'type Obligation struct' internal/domain/types.go` = строка 295; `grep -n -i revoke internal/domain/*.go` даёт только комментарий ScopeStop в capabilities.go:185, события ревока нет; `git show origin/main:cmd/beacon/inspect.go | grep -c 'Obligation\|ladder\|Marker'` = 0; `git show origin/main:internal/domain/types.go | grep -n 'type Obligation struct'` = строка 295.
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/ladder/transitions.go, internal/infra/ladder/escalation.go, internal/infra/ladder/transitions_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Различать отказ доставки из-за промпта, лимита, исчезнувшей панели и отсутствия человека, с полями deliveryRung, deliveryAttempts и nextRetryAt.
Контекст: Premise: проверено 2026-11-21, жёсткий лимит в шесть wake и ESCALATIONS.md есть, но ступени доставки не поименованы. baseline: `grep -rn 'deliveryRung\|nextRetryAt' internal/infra/ladder` пусто. Источник: OpenRig queue.wake_human_rung и Claude workflows с лимитом пять рестартов.
Шаги: Добавить поля ступени и времени следующей попытки в обязательство. Разрешить только одно активное human alert на инцидент. Тест на четыре отказа доставки: в чат уходит одно уведомление, повторы привязаны к revoke-событию.
Проверка: gofmt чистый. Тесты в CI. Счётчик wake по Watch не меняется.
Приёмка: Лестница доставки видна в beacon inspect. Повтор не создаёт новое уведомление человеку.

## Подтверждение

Проверено 2026-11-21 по `git log`, `gh pr view` и `git grep` на свежем origin/main. Реализация влита PR #877, https://github.com/refusedguy/beacon/pull/877. Merge commit 961f83529f5798a2dd5f4a1df48c5999a2a8713c. Дубль RS2-CMP-03 закрыл те же шаги.

Три файла из прежнего блокера обновлены. `internal/domain/types.go:319`, `:323` и `:326` содержат `DeliveryRung`, `DeliveryAttempts` и `NextRetryAt`. `internal/domain/transition.go:90` и `:224` добавляют `DeliveryRevoke` и `RevokeEvent`. `cmd/beacon/inspect.go:64` и `:243` показывают лестницу доставки в выводе inspect. Ладдер записывает поля и обрабатывает ревока в `internal/infra/ladder/transitions.go:348`, `:463` и `:499`, тесты лежат в `internal/infra/ladder/transitions_test.go:182`. Premise выше описывала состояние до PR #877, блокер снят. Статус переведён в done. Код, wiring и .beacon не трогал.
