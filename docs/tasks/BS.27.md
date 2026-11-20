# BS.27. Маппинг webhook на Watch
Статус: done
Premise: проверено 2026-11-20, дубль. Функционал уже влит в origin/main через PR #483 (fix/bs27, commit ce91ff8): internal/infra/webhook/match.go и match_test.go. RoutingKey требует repo+PR+head_sha (Valid), matchWatch сравнивает все три поля и не трогает состояние, MapEvent пишет намерение refresh через RefreshIntent.RecordRefresh и не публикует verdict, no-match и неполный ключ логируются на info (reasonNoMatch/reasonIncomplete) с nil-ошибкой. Тесты покрывают совпадение, чужой PR, устаревший head SHA, пустой source, ошибки source и intent, неполный ключ. OwnerFiles bridge.go уже содержит refresh-цикл Bridge; новый код создал бы вторую копию механизма. Дубль не создан.
Фаза: P2
Тип: task
Владелец файлов: internal/infra/webhook/match.go, internal/infra/cibridge/bridge.go
Зависимости: BS.26
Ссылка: ADR 0193
Агент: нет
Время: нет
Цель: Событие находит нужный Watch.
Контекст: Ключ: repo + PR + head_sha.
Шаги: Извлечь поля, найти Watch, записать намерение refresh, не публиковать verdict.
Проверка: Событие от чужого PR не меняет состояние.
Приёмка: Отсутствие совпадения логируется, а не ошибка.

## Подтверждение
- Premise проверен повторно 2026-11-20: функциональность уже в main (PR #483, `ce91ff8`; статус done в PR #595, `d7780de`). `internal/infra/webhook/match.go` содержит `RoutingKey` с полями repo/PR/head_sha и методом `Valid`, чистую функцию `matchWatch`, сравнивающую все три поля без изменения состояния, и `MapEvent`, который пишет намерение refresh через `RefreshIntent.RecordRefresh`, не публикует verdict и логирует no-match и неполный ключ на info (`reasonNoMatch`/`reasonIncomplete`) с nil-ошибкой. Тесты в `match_test.go` покрывают совпадение, чужой PR, устаревший head SHA, пустой source, ошибки source и intent, неполный ключ и чистоту `matchWatch`. `internal/infra/cibridge/bridge.go` уже содержит refresh-цикл `Bridge`. Проверено наличным кодом в `origin/main`, дубль в OwnerFiles не создан.
