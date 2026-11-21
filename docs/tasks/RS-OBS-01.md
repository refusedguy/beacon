# RS-OBS-01. Корреляционные поля в структурированных логах цикла
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/observe/log.go, internal/infra/observe/log_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Добавить watchID, obligationID, revision, role, adapter, verdict и attempt в каждую запись цикла и проверить связность по cycleID.
Контекст: Premise: проверено 2026-11-21, cycleID есть, но остальные идентификаторы в логи цикла не попадают. baseline: `grep -c 'cycleID' internal/infra/observe/log.go` = 0, `grep -rc 'watchID' internal/infra/observe/log.go` = 0. Источник: OpenTelemetry GenAI semantic conventions.
Шаги: Расширить структуру записи и точки вызова. Не писать ses_... или obligationID в agent.name. Не генерировать conversation.id из UUID или хеша. Добавить тест, который по одному cycleID находит все связанные записи и квитанцию.
Проверка: gofmt чистый. Тесты в CI. Кардинальность agent.name остаётся низкой.
Приёмка: По cycleID находится полная цепочка от Watch до доставки. Текст уведомления по умолчанию в лог не пишется.

Premise: проверено повторно 2026-11-21. Поля watchID, revision, role, adapter, verdict и cycleID уже определены константами в internal/infra/observe/observe.go и пишутся в события. Тест связности по cycleID уже есть: TestW2ACycleIDTagsObserveEvents в internal/infra/cibridge/w2a_test.go. Baseline из Контекста подтверждён для OwnerFiles: в log.go нет литерала cycleID и нет упоминаний watchID, кроме одного комментария в строке 64.
Блокер: структурная запись цикла создаётся в internal/infra/cibridge/bridge.go функцией cycleEvent и её вызовами, а константы полей лежат в internal/infra/observe/observe.go. Оба файла не входят в OwnerFiles карточки. Мои OwnerFiles, internal/infra/observe/log.go и log_test.go, только строят логгер, редактируют секреты, переименовывают cycleID в cycle_id и разбирают fence отчётов. Они не создают запись цикла, поэтому добавить поля и тест связности в них нельзя. Часть запрошенных полей уже есть в main, оставшиеся obligationID и attempt появляются только в cibridge. Нужно решение владельца: передать карточку владельцу cibridge/bridge.go и observe/observe.go или изменить OwnerFiles. Код не писал, gofmt не применял, дубль не создал. Возвращаю точный блокер оркестратору.

## Подтверждение
- PR #875 MERGED: 2026-11-21 (squash).
