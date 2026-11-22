# WIRING-1822. Connect hookrun modes to card lifecycle events in service.go

Статус: deferred
Фаза: P3
Тип: task
Владелец файлов: internal/app/service.go
Зависимости: нет
Ссылка: W12-B
Агент: watch:refusedguy/beacon:1822:6b40cbd9775521c49c013bce7076af094efa3410@1795361075:1800
Время: нет
Цель: Connect hookrun modes to card lifecycle events in service.go

Контекст: Wiring для PR #1822 отложен из Watch watch:refusedguy/beacon:1822:6b40cbd9775521c49c013bce7076af094efa3410@1795361075:1800. Причина: CD-05 adds Notify, Sync and Gate in internal/infra/hookrun/modes.go but the card forbids editing service.go in this PR. The app layer must choose a mode per claimed/unblocked/closed event and pass a Runner, which is a separate OwnerFiles boundary.

Шаги: Перенести wiring в отдельный PR и обновить контракт инструментов.
Проверка: Новый инструмент и CLI-команда работают на зелёном CI.
Приёмка: Wiring для PR #1822 выполнен, карточка WIRING-1822 закрыта.
