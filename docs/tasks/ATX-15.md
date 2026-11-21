# ATX-15. Дождаться detached goroutine webhook в Server.Close
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/webhook/receiver.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не закрывать store, пока запись webhook ещё исполняется.
Контекст: Источник: docs/research/concurrency-audit.md, раздел 3 и строка таблицы 134. internal/infra/webhook/receiver.go:137 запускает go h.record(d) с context.Background(). Server.Close в :255 зовёт httpServer.Shutdown, который ждёт только HTTP handlers. Отсоединённая горутина не входит в ожидание. Webhook получает 202, Close возвращается, а record ещё пишет в store. При медленном recorder число goroutine растёт без границы. Приоритет высокий в отчёте.
Шаги: Добавить sync.WaitGroup в Receiver. Add перед go h.record, Done в record. В Server.Close после Shutdown ждать группу с таймаутом. Либо заменить горутину на один worker с ограниченной очередью.
Проверка: CI зелёный. Тест с блокирующим recorder подтверждает, что Close дожидается всех N записей. go test -race не показывает рост goroutine.
Приёмка: Close возвращается только после завершения всех record. Запись в закрытый store исключена.
