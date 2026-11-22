# WIRING-1814. Подключить контракт окружения хука к RunHooks
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/app/taskcli/service.go, internal/infra/hookrun/hookrun.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1814
Ссылка: PR 1814
SourceWatch: watch:refusedguy/beacon:1814:bde394bfdf8a4ecd1f14819d9281ed906e618c5f@1795360933:1800
PR: 1814
Агент: нет
Время: нет
Цель: Подключить контракт окружения хука к RunHooks
Контекст: CD-06 задаёт контракт окружения в internal/app/taskcli/hook_envelope.go. Функции Service.RunHooks и hookrun.Run не входят в OwnerFiles карточки и пока не передают BEACON_* переменные сценарию.
Шаги: Дождаться мержа PR 1814; собрать HookEnvelope в Service.RunHooks из карточки, Watch, PR, ветки и worktree; передать BEACON_* переменные в окружение процесса при запуске сценария через hookrun.Run; не копировать родительское окружение целиком; вызвать `beacon deferred done` для записи с watchID из SourceWatch.
Проверка: Сценарий события мержа получает BEACON_TARGET_BRANCH, сценарий закрытия его не получает; обязательные поля заполнены; секреты родительского процесса не попадают в окружение сценария; локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.
Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

## Причина

Контракт окружения и его тесты лежат в OwnerFiles CD-06, а точки подключения находятся в чужих OwnerFiles. Подключение меняет service.go и hookrun.go и выходит за скоуп PR 1814, поэтому оно отложено отдельной карточкой.
