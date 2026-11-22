# ATX-R07. Перевести хелперы ожидания TUI на контекст теста
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/tui/socket_test.go, internal/tui/client_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать фиксированные двухсекундные таймауты из общих хелперов TUI-тестов.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.2 и строка таблицы 158, и docs/research/flaky-tests-remaining.md, строка таблицы 7. `nextEvent` в `internal/tui/socket_test.go:58-70` и `nextRequest` в `internal/tui/client_test.go:59-68` ждут события через `time.After(2 * time.Second)`. Оба хелпера вызываются во многих тестах, поэтому один медленный прогон умножает задержку. `sendRequest` в `socket_test.go:49` уже использует `context.WithTimeout`. Каталог Unix-сокета в `:19` создаётся через `os.MkdirTemp` и очищается через `t.Cleanup`, эту часть менять не нужно. baseline: два хелпера по 2 с.
Шаги: Создать общий хелпер ожидания, который принимает `t.Context()` и явный таймаут. Перевести на него `nextEvent` и `nextRequest`. В каждом тесте задавать дедлайн через `context.WithTimeout` и отменять его через `t.Cleanup`. Убрать `time.After(2 * time.Second)` из обоих файлов.
Проверка: CI зелёный. В `socket_test.go` и `client_test.go` нет `time.After(2 * time.Second)`. Все тесты TUI завершаются по общему контексту.
Приёмка: Время ожидания задаётся тестом. Один общий хелпер исключает расхождение между сокетными и HTTP-тестами.

Итог: Already landed in PR #1639 (commit e3179ca123cd04405825772ef55f83fbdd2a91df): test(tui): wait helpers bounded by t.Context (ATX-R07)
Доказательство: PR #1639; commit e3179ca123cd04405825772ef55f83fbdd2a91df; git merge-base --is-ancestor e3179ca123cd04405825772ef55f83fbdd2a91df origin/main exit 0; socket_test.go and client_test.go have no time.After(2 * time.Second) left; the wait helpers take a context deadline from the test.
Причина: already landed in PR #1639 e3179ca123cd
Время закрытия: 2026-11-22T11:53:03Z
