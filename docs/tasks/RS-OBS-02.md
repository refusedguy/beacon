# RS-OBS-02. Имена спанов по GenAI конвенциям в локальном экспортере
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/otel/local.go, internal/infra/otel/local_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Привести имена и атрибуты локальных спанов к конвенциям: operation.name, provider.name, tool.name, tool.call.id и tool.type.
Контекст: Premise: проверено 2026-11-21, локальный экспортер пишет спаны в NDJSON без GenAI атрибутов. baseline: `grep -c 'gen_ai' internal/infra/otel/local.go` = 0. Источник: gen-ai-agent-spans.md и execute-tool.md.
Шаги: Добавить константы атрибутов и функцию их заполнения. Для доставки wake использовать tool.name вида wake.opencode и tool.type extension. Не складывать токены из разных спанов. Тест на обязательные поля и на отсутствие содержимого сообщений.
Проверка: gofmt чистый. Тесты в CI. Размер процесса не вырастает за счёт SDK.
Приёмка: Спан по имени и атрибутам совпадает с конвенцией. Содержимое по умолчанию не попадает в экспорт.

Premise: проверено повторно 2026-11-21. Атрибутов gen_ai в Go-коде нет: поиск по internal/ и cmd/ даёт 0 совпадений, origin/main совпадает с HEAD. Блокера по OwnerFiles нет. Спан доставки wake создаётся в internal/infra/wake/deliver.go, но local.go получает готовый observe.Span с именем beacon.wake.deliver и атрибутом obligation_id. Этого достаточно, чтобы заполнить конвенцию в самом экспортере.

Решение: добавлены константы пяти атрибутов GenAI и функция заполнения в internal/infra/otel/local.go. Спан wake экспортируется как execute_tool wake.opencode с атрибутами gen_ai.operation.name=execute_tool, gen_ai.provider.name=opencode, gen_ai.tool.name=wake.opencode, gen_ai.tool.call.id из obligation_id и gen_ai.tool.type=extension. Токены между спанами не складываются, атрибутов usage нет. Атрибуты с текстом сообщений и аргументами инструмента в экспорт не попадают. Тесты на обязательные поля, отсутствие содержимого и раздельные call id добавлены в local_test.go. gofmt применён. go test локально не запускался, проверка идёт в CI. OTel SDK не добавлен.

## Подтверждение
- Premise перепроверен 2026-11-21 по свежему origin/main (527084c). Реализация влита PR #854, коммит 43d2788, merged 2026-11-21. В internal/infra/otel/local.go на main определены пять констант GenAI-атрибутов, функция fillGenAIAttrs и список contentAttrs с ключами для текста сообщений и аргументов инструмента. Спан beacon.wake.deliver переименовывается в execute_tool wake.opencode. Атрибут gen_ai.tool.type равен extension, gen_ai.tool.call.id берётся из obligation_id. Токены между спанами не складываются, ключи с текстом в экспорт не попадают. Тесты TestWakeSpanUsesGenAIConventions и TestWakeSpansKeepSeparateCallIDsAndNoTokenSum в local_test.go на main закрывают обязательные поля, отсутствие содержимого и раздельные call id. Открытых PR по карточке нет, дубля нет. Статус изменён с todo на done со ссылкой на PR #854. OwnerFiles не менял, код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.
