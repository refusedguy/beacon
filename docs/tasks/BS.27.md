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
