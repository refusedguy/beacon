# WIRING-1822. Connect hookrun modes to card lifecycle events in service.go

Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/app/service.go
OwnerFiles: internal/app/service.go
Зависимости: PR 1822
Ссылка: PR 1822
SourceWatch: watch:refusedguy/beacon:1822:8cd43c41fdfbdd6ad145f865326a1900aa7793b0@1795361182:1800
PR: 1822
Агент: нет
Время: нет
Цель: Подключить режимы hookrun к событиям карточки в internal/app/service.go.

Контекст: CD-05 добавила режимы Notify, Sync и Gate в internal/infra/hookrun/modes.go. Карточка запрещает править service.go в этом PR, поэтому приложение пока вызывает только hookrun.Run без выбора режима.

Шаги: Дождаться мержа PR 1822; выбрать режим для событий claimed, unblocked и closed; внедрить Runner в service.go; вызвать `beacon deferred done` с watchID из SourceWatch.

Проверка: Карточка docs/tasks/WIRING-1822.md попала в PR; локальный гейт WIRING-* проходит: статус todo, OwnerFiles заполнен, раздел «Причина» есть.

Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет status done, CI зелёный.

## Причина

Точка подключения лежит в internal/app/service.go, который не входит в OwnerFiles карточки CD-05. Выбор режима для каждого события и внедрение Runner требуют отдельного PR и решения владельца по контракту вызова.
