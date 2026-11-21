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
Контекст: Premise: владелец прямо сказал: Beacon когда замерджил, задачу в дон перевёл.
Шаги: Вызвать закрытие в applyMerged/write через порт card links, записать PR в подтверждение, тест на merged и на hold.
Проверка: gofmt, go test в CI.
Приёмка: После merged карточка done с ссылкой на PR, при hold статус не меняется.
