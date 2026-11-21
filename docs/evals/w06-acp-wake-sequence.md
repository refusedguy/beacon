# W06: ACP wake sequence

## Сценарий

Beacon будит агента с нативной поддержкой ACP, имея только сохраненный sessionId и текст ответа по обязательству. Канал wake подтверждается живым сеансом, а не только handshake.

## Baseline

Источник: docs/research/e2e-coverage-audit.md, строка 9 таблицы. До изменений acp_smoke_test.go вызывал три метода: initialize, session/new и session/load. В живом сеансе не было session/prompt, resume и проверки входящих session/update. Сценарий W06 существовал только в документах. Приоритет P2.

## Проверки

- initialize согласует protocolVersion и читает agentCapabilities, включая loadSession.
- session/new возвращает непрозрачный sessionId.
- При объявленной capability loadSession перед prompt вызывается session/load. Без этой capability адаптер создает новую сессию через session/new.
- session/prompt передает текст wake в сессию и возвращает непустой stopReason.
- До ответа session/prompt приходит хотя бы одно уведомление session/update. Это доказывает, что агент начал работу, а не только принял запрос.
- Тест управляет внешним бинарником из OPENCODE_BIN. Без бинарника тест пропускается.

Живой тест: TestOpenCodeACPWakeSequence в internal/infra/wake/acp_smoke_test.go.

## Негативный контроль

Только initialize и session/load без session/prompt. Агент подключается к сессии, но новую работу не начинает. Проверка фиксирует, что handshake и resume сами по себе не являются wake.

## Покрытие

Тест доводит живой ACP-сеанс от handshake до prompt и resume. Доставку wake подтверждают входящие session/update, а не только успешный RPC-ответ.
