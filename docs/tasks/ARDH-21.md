# ARDH-21. Свести два формата ошибки HTTP к одному типу
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/server/http/error.go, internal/infra/httpapi/server.go
Зависимости: 
Ссылка: ADR 0009
Агент: нет
Время: нет
Цель: Пусть httpapi.writeError делегирует общему типу Error и WriteError без изменения формата ответа.
Контекст: Источник: docs/research/error-handling-audit.md, находка 4 и рекомендация 5. В httpapi/server.go:725 свой errorBody с строковыми кодами и 12 вызовов writeError. В server/http/error.go есть Code, Error, CodeOf и WriteError. Риск: низкий. Формат ответа должен остаться прежним.
Шаги: Сопоставить коды и поля обоих форматов. Добавить делегирование. Обновить тесты HTTP адаптера на прежний JSON.
Проверка: CI зелёный. Тесты HTTP ответов проходят без изменения ожидаемого тела.
Приёмка: Один тип ошибки и одна функция записи, формат ответа не изменился.

## Причина
Карточка написана по коммиту d3b1bfb. Тогда `internal/infra/server/http/error.go` содержал `Code`, `Error`, `CodeOf` и `WriteError`. Коммит d4f7b326 (CLEAN-02) удалил пакет `httproute` как неиспользуемый. В `origin/main` на коммите b9937c01 этого файла нет. Второго формата ошибки тоже нет. Остался только `httpapi.errorBody` с полями `ok`, `code` и `message`.

Делегировать некому. Два формата и раньше не совпадали по байтам. У `httproute.Error` не было поля `ok`. Коды были в нижнем регистре, например `invalid_argument` и `unauthorized`. Статус выводился из кода через `Code.Status()`. Content-Type был `application/json`. У `httpapi` коды пишутся в верхнем регистре, например `UNAUTHORIZED` и `INVALID_INPUT`. Статус передаёт каждый вызов `writeError`. Тело содержит `"ok":false`. Content-Type равен `application/json; charset=utf-8`. Возврат удалённого пакета вернул бы мёртвый код и изменил тела ответов.

Код не менялся. Формат зафиксирован тестом `internal/infra/httpapi/error_format_test.go`. Тест проверяет статус, Content-Type и точные байты тела. Проверки `go test`, `go build` и `beacon gates` запускает CI на PR.
