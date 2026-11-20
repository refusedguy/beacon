# BS.29. Привязка фоновой задачи к чату
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/scheduler/binding.go
Зависимости: BS.24
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: Результат задачи приходит в нужную сессию.
Контекст: T3 bindToCurrentThread по умолчанию true только при наличии thread в проекте.
Шаги: Поле bind_target, проверка проекта, отказ при явном true без thread.
Проверка: Непривязанная задача создаёт новую сессию.
Приёмка: Рецепт wake использует target из binding.

## Подтверждение
- premise проверен 2026-11-20: дубль. Модель привязки расписания к цели уже в main через PR #454 (merge 2148903). `internal/app/scheduler/binding.go` задаёт `Binding` с полем `bindTarget`, а `Bindings` хранит одну привязку на расписание через `Bind`, `Unbind`, `Get`, `TargetOf`, `List`. `TargetOf` возвращает `(target, false)` для непривязанного расписания, то есть wake создаёт новую сессию. `binding_test.go` покрывает сохранение, повтор без дубля, конфликт, снятие привязки и пустой набор. Доставка в нужную сессию уже есть в `internal/infra/wake/registry.go` (`SubscriberTarget`, `Deliver`) и `internal/infra/store/watch_bindings.go`. Файл в main совпадает с PR #454 байт в байт. OwnerFiles не менял, чтобы не дублировать принятую модель.
