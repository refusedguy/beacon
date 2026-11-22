# PROTO-01. Закрепить одиннадцать advertised-имен и проверить remote-карту
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/contract_test.go, tests/e2e/e2e_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать так, чтобы контракт tools/list и remote-режим проверяли один и тот же каталог имён, включая beacon_wake.
Контекст: Источник: docs/research/audit-04-protocols.md, раздел «Измеренное состояние». Baseline: 10 дескрипторов, 11 advertised имен, 9 в документации, 7 в remote-карте. AdvertisedToolNames в internal/infra/mcp/server.go:258-262 добавляет beacon_wake, зарегистрированный в cmd/beacon/mcp.go:521. E2E-тест берёт ожидаемый набор в tests/e2e/e2e_test.go:59. Удалённый клиент не знает beacon_wake и три инструмента из десяти. AUD-09 и AUD-10 не меняются.
Шаги: Вывести ожидаемый набор из одного каталога. Добавить проверку, что каждое имя из этого набора имеет непустое описание и входную схему в remote-карте. Добавить проверку, что beacon_wake не попадает в remote-режим как инструмент агента.
Проверка: CI зелёный. Тест падает при добавлении имени без описания или без remote-фабрики. E2E-тест использует тот же каталог.
Приёмка: Изменения только в OwnerFiles карточки. AUD-09, AUD-10, newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и ATX-R01 не переделываются.
