# FTE-08. Возобновлять Watch в HTTP-пути beacon human
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
Зависимости:
Ссылка: docs/evals/w21-human-question-revision.md:4034-4054, docs/evals/e05-wait-pending-question.md:762-784, docs/research/research-feature-gaps.md:17,73
Агент: нет
Время: нет
baseline: humanDecisionSink.Apply в cmd/beacon/mcp.go:982 меняет состояние только для части команд. Для approve, reject, rework, context и force_retry_merge ветки нет. humanDecisionSink.wake в :1056 шлёт wake с вердиктом human:<verb> после того, как Watch остался в needs_input.
Цель: Ответ человека в HTTP-пути возвращает ту же Watch в watching с ростом ревизии на один и доставляет wake подписчику.
Контекст: docs/evals/w21-human-question-revision.md:4034 описывает ответ approve в ту же Watch через ack с continue_of или decision. Строки 4044-4048 требуют тот же ID, статус watching, ревизию на один больше и claimedAt на обязательстве. Локальный путь в cmd/beacon/human.go:187-214 это делает. HTTP-путь шлёт wake, но состояние не меняет, поэтому разблокируемый wake не находит.
Шаги: В humanDecisionSink.Apply вызвать тот же ack-порт, что использует локальный путь, для approve, reject, rework и context. Сохранить wake после успешного перехода. Передавать continueOf из Watch. Не менять форму WakePayload для команд, которые уже работают. Покрыть тестом POST approve на Watch в needs_input.
Проверка: После POST approve Watch имеет статус watching, ревизию на один больше, тот же ID и доставленный wake. Обязательство несёт claimedAt. Повторный ответ идемпотентен и не сдвигает ревизию. Неизвестный verb возвращает отказ без записи.
Приёмка: Локальный и HTTP-пути дают одинаковый переход, CI зелёный.
