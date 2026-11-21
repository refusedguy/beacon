# RS-WK-04. Уже обработанная доставка как успешный ответ
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/webhook/receiver.go, internal/infra/webhook/receiver_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Premise: проверено 2026-11-21 по origin/main (6ece091), дубль. Поведение «уже обработанная доставка даёт успешный ответ без повторной публикации» уже в main. В receiver.go:98-114 подпись проверяется до дедупликации. В receiver.go:137-139 приёмник отвечает 202 до записи в журнал. В receiver.go:159-164 повтор помечается как webhook-duplicate и не вызывает публикацию. WorkflowHook.ServeHTTP в workflow_run.go:191-200 тоже отвечает 202 duplicate без вызова sink, а проверка подписи стоит раньше в workflow_run.go:156. Добавление слова Duplicate в receiver.go не изменит поведение, которое уже зафиксировано кодом.
baseline: `grep -c 'Duplicate' internal/infra/webhook/receiver.go` = 0; `grep -n 'Duplicate\|Conflict' internal/infra/store/webhook_events.go` = строки 46, 47, 52, 53, 95, 143, 144; `grep -rn '\.RecordDelivery(' --include='*.go' . | grep -v '_test.go' | wc -l` = 0; `git log --all --oneline -- docs/tasks/RS-WK-04.md` = 6ece091 и f457f03, оба batch-apply; `gh pr list --repo refusedguy/beacon --state all --search 'RS-WK-04'` = 0 PR; `git rev-parse --short origin/main` = 6ece091.
Цель: Возвращать успех для уже обработанного события, чтобы остановить ретраи провайдера.
Контекст: Premise: проверено 2026-11-21, RecordDelivery различает Created, Duplicate и Conflict, но поведение приёмника для Duplicate не зафиксировано контрактом. baseline: `grep -n 'Duplicate' internal/infra/store/webhook_events.go` находит результат, `grep -n 'Duplicate' internal/infra/webhook/receiver.go` пусто. Источник: Stripe manage-webhook-endpoints.
Шаги: Явно обработать Duplicate как успешный ответ без повторной публикации. Не путать с Conflict по телу. Тест на повтор с тем же X-GitHub-Delivery и на изменение тела.
Проверка: gofmt чистый. Тесты в CI. Подпись проверяется до дедупликации.
Приёмка: Провайдер прекращает ретраи после первого успешного эффекта.

## Блокер

- 2026-11-21. Две причины вернуть блокер без кода.

  Поведение уже в main. Приёмник отвечает 202 до записи и не публикует ничего при повторе. Подпись проверяется раньше дедупликации. Повтор с тем же X-GitHub-Delivery закрыт тестом `TestReceiverRedeliveryIsIdempotent` в receiver_test.go:275-290. Второй обработчик, WorkflowHook, явно отвечает 202 duplicate без вызова sink. Новый тест на тот же сценарий повторил бы существующую проверку.

  Честное подключение результата RecordDelivery к приёмнику требует чужих OwnerFiles. Порт `DeliveryRecorder.Record` в receiver.go:48-49 возвращает только `claimed bool`. Флагов Duplicate и Conflict в нём нет. Единственный рабочий адаптер `webhookDeliveryRecorder` живёт в cmd/beacon/mcp.go:787-796, а дедупликацию ведёт `MemoryDeliveryJournal.ClaimDelivery` в internal/infra/webhook/workflow_run.go:54-79. Оба файла вне OwnerFiles карточки. `RecordDelivery` лежит в internal/infra/store/webhook_events.go:97-152 и не имеет ни одного рабочего вызывающего. Адаптер store для приёмника нельзя положить в internal/infra/webhook: AGENTS.md запрещает infra импортировать соседнюю infra. Нужен файл в cmd/beacon или internal/app. Тест на изменение тела требует, чтобы порт различал Duplicate и Conflict. С булевым портом изменение тела даёт тот же `claimed=false`, что и повтор с прежним телом, поэтому отделить Conflict от Duplicate внутри OwnerFiles нельзя.

  Статус todo сохранён. Код не писал, gofmt не применял, OwnerFiles не менял, чужие файлы и wiring/.beacon не трогал. Блокер возвращён оркестратору: для карточки нужны OwnerFiles с cmd/beacon/mcp.go и internal/infra/webhook/workflow_run.go либо переформулировка шагов под уже существующее поведение.
