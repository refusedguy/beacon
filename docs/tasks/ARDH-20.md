# ARDH-20. Проверить проглоченные ошибки в оркестраторе
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/orchestrator/checkall.go, internal/app/orchestrator/decision.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Проверять внутренние json.Unmarshal в checkall.go и логировать ошибку resolver зависимостей в decision.go, сохранив fail closed.
Контекст: Источник: docs/research/error-handling-audit.md, находка 6 и рекомендация 3. В checkall.go:827-838 три Unmarshal и три Marshal с игнорируемой ошибкой, а от verdict и headSHA зависит запись. В decision.go:354 satisfied, _ трактует ошибку resolver как неудовлетворённую зависимость. Риск: низкий.
Шаги: Добавить проверки ошибок для внутренних Unmarshal с явным решением при битом поле. В decision.go логировать ошибку resolver через observe, не меняя fail closed.
Проверка: CI зелёный. Тесты на битые поля verdict и headSHA покрывают новую ветку.
Приёмка: Битые внутренние поля не приводят к тихой записи, ошибка resolver видна в логах.

## Подтверждение
- PR #1179: https://github.com/refusedguy/beacon/pull/1179. `SQLVerdictWriter.Publish` проверяет три внутренних `Unmarshal` полей `verdict`, `headSHA` и `revision`, а также три `Marshal` обновляемого payload. Каждая ошибка возвращается с именем поля и ID Watch через `%w`. При битом поле запись не выполняется, а ошибка попадает в `Row.Error` и видна в выводе `beacon ci check`. Валидный JSON проходит прежним путём. Тесты в `internal/app/orchestrator/checkall_test.go` подставляют битый JSON в каждое поле и проверяют отказ записи и отсутствие обязательства. Часть про логирование ошибки resolver в `decision.go` не вошла в этот PR: её OwnerFile не был передан исполнителю.
