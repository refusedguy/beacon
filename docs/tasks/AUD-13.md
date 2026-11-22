# AUD-13. Починить красный test на main: noChecksTimeout и golden tools/list
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/contract_test.go
Зависимости: 
Ссылка: CI run 44983833077 job 137089954830, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Вернуть main зелёным: покрыть поле domain.Watch.noChecksTimeout в DTO и обновить golden tools/list.
Контекст: Источник: отчёты агентов PR 1397 и 1403, воспроизводится на main 9e29d9f. `TestListRunningWatchOutputFields` в contract_test.go:688 падает: domain.Watch field "noChecksTimeout" is not covered by WatchOutput or the unexposed list. `TestToolsListStabilityAcrossConnections` падает: tools/list 19467 байт против golden 19504, SHA не совпадает. Причины — коммиты main 07e4c15 и 19ca199, добавившие NoChecksTimeout в domain и觀察. baseline: 2 красных теста на main.
Шаги: Добавить поле noChecksTimeout в WatchOutput DTO в schema.go (или явно внести его в список неэкспонируемых полей контрактного теста, если поле не должно наружу). Пересчитать toolsListByteCount и toolsListGoldenSHA256 по актуальной сериализации. Добавить тест, который падает при的新ом поле domain.Watch без решения в DTO.
Проверка: CI зелёный на main. TestToolsListStabilityAcrossConnections и TestListRunningWatchOutputFields проходят. Никаких правок в PR-агентских файлах.
Приёмка: Только OwnerFiles карточки. Не трогать ROADMAP_STATUS (это AUD-14).
