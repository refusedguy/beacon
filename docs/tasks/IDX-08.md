# IDX-08. Автоматический перевод карточки в done при мерже
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/cibridge/bridge_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Терминальный вердикт merged закрывает связанную карточку без агента.
Контекст: Premise: владелец прямо сказал: Beacon когда замерджил, задачу в дон перевёл. baseline: grep -rlE 'CardLinks|card_links|CloseCard|MarkCardDone' --include='*.go' . | wc -l даёт 0. Файл internal/infra/store/card_links.go отсутствует. IDX-07 со статусом todo владеет этим файлом.
Шаги: Вызвать закрытие в applyMerged/write через порт card links, записать PR в подтверждение, тест на merged и на hold.
Проверка: gofmt, go test в CI.
Приёмка: После merged карточка done с ссылкой на PR, при hold статус не меняется.

## Блокер

Порт card links отсутствует, поэтому закрытие карточки в applyMerged вызвать нечем. В коде нет интерфейса CardLinks и метода закрытия карточки. Реализация связи карточки с Watch принадлежит IDX-07 и находится в internal/infra/store/card_links.go. Этого файла нет, статус IDX-07 равен todo. Код в OwnerFiles IDX-08 не меняется, чтобы не добавлять порт без реализации.

Точный блокер: internal/infra/store/card_links.go, владелец IDX-07, статус todo.
