# RS-WK-02. Явный идентификатор уведомления в beacon-event
Premise: проверено 2026-11-21 на origin/main d3b1bfb. `domain.ObligationKey` уже считается из watchID, revision и subscriberID, и `WakePayload.ObligationID` хранит это значение. Все три сборщика конверта в wake-адаптере строят текст без явного поля id, поэтому адаптер не может дедуплицировать уведомление по его ключу до steer.
baseline: до правки `grep -n '<beacon-event>' internal/infra/wake/deliver.go` даёт строки 281, 691, 1199. Ни один из трёх конвертов не содержит поля id. Аналог: dedup_key в PagerDuty.
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/wake/deliver.go, .opencode/plugins/beacon-bridge.js
Зависимости: 
Ссылка: 
Агент: executor t9
Время: 2026-11-21
Цель: Передавать ObligationKey как явный id уведомления и дедуплицировать повторы по этому ключу на стороне адаптера.
Контекст: Premise: проверено 2026-11-21, ObligationKey уже считается как watchID плюс revision плюс subscriberID, но в конверте нет явного поля id. baseline: `grep -n '<beacon-event>' internal/infra/wake/deliver.go` = строки 281, 691, 1199. Аналог: dedup_key в PagerDuty.
Решение: Все три сборщика конверта теперь кладут `"id": <ObligationID>`, который уже равен ObligationKey. Подпись Deliver не менялась. Плагин читает явный id через `notificationID`, пишет его в конверт и проверяет до вызова steer: S2-канал дедуплицирует по `notificationID(item)` через существующий `claim`, review-канал через `claim(dedupID)` до prompt. Claim лежит в каталоге состояния и переживает рестарт клиента. При transport-failed claim снимается, чтобы вердикт, который агент не прочитал, мог уйти повторно.
Шаги: Добавлено поле id в Envelope, T202Envelope и T212buildPromptText. Подпись Deliver не тронута. В плагине добавлены `notificationID`, id в конвертах buildPromptText и deliverReviewPointer, проверка id до steer в deliverEvent и deliverReviewPointer. Добавлены JS-тесты на явный id, на повтор конверта после рестарта клиента и на S2-повтор после рестарта плагина.
Проверка: gofmt -w применён к deliver.go, `gofmt -l` пуст. Локально запущены `node --test .opencode/plugins/beacon-bridge.js`: 19 тестов, 19 прошли. Промпт по уже прочитанному вердикту не шлётся: повторный review даёт outcome duplicate и не вызывает prompt. Локальные go test и go build не запускались по правилам AGENTS.md, проверка идёт в CI PR.
Приёмка: Рестарт клиента не вызывает повторный steer по прочитанному обязательству. Тест с новым ctx и тем же каталогом состояния подтверждает ноль повторных prompt.

## Подтверждение
Premise подтверждена замером: до правки три сборщика конверта не несли явного поля id, хотя ObligationKey уже был доступен как `p.ObligationID`. gofmt чистый. 19 JS-тестов прошли локально, включая три новых RS-WK-02. Харнесс eval для wake (scenario 12 и 18) прогнан против изменённого плагина: доставка steer, квитанция posted 0600, повтор не меняет файл, outcomes posted и transport-failed. Блокера нет, чужие OwnerFiles не тронуты.
