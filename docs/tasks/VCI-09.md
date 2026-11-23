# VCI-09. Переводить сессию агента в idle по хуку Stop

Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/event/bus.go, internal/infra/observe/agent_turn_event.go, internal/infra/observe/agent_turn_event_test.go
Зависимости: нет
Ссылка: https://github.com/refusedguy/beacon/pull/2133; /tmp/opencode/backlog/04-verdict-ci.md, строка VCI-09
Агент: ses_df8eb3689ffewpWdRZpiwcfcR5
Время: 2026-11-23T06:29:29Z

Цель: Перевести сессию агента в idle по хуку Stop. Переход должен быть идемпотентным и не менять протокол Watch, правила revision и поля delivered, claimed, read.

Контекст: Проверка premise по свежему origin/main показала, что функция уже влита. PR #2133 добавил `AgentTurnTracker` в `internal/infra/observe/agent_turn_event.go`. Парсер принимает имя события в полях `event` и `hook_event_name`. Ключ дедупликации состоит из pane id и turn id. Событие чужого pane и невалидный JSON отклоняются. Переход записывает статус `idle`. Упомянутый в исходной карточке парсер в `internal/app/event/bus.go:47,51` в main отсутствует, реализация лежит в `internal/infra/observe/agent_turn_event.go`.

Проверка premise выполнена по командам из шаблона исполнителя. Полный SHA коммита PR 2133 равен `9179dd5111102e9b7c5502d8400b5c7e2f61c2bd`. Команда `git merge-base --is-ancestor 9179dd5111102e9b7c5502d8400b5c7e2f61c2bd origin/main` вернула код 0. База проверки: `origin/main` `79dc16941bdbb5ff312f85e8602dda0e67be1a2a`. Дублирующий код не написан.

Правка LEAK-09 в `agent_turn_event.go` лежит в коммите `8fe21423` на ветке `fix/leak09` и в main не влита. Ветка этой карточки построена от свежего `origin/main`, поэтому откаты не было.

Шаги: Проверить premise по свежему `origin/main`. Получить полный SHA коммита PR 2133. Подтвердить вхождение коммита в main через `git merge-base --is-ancestor`. Сверить код и тесты с критериями приёмки. Не писать дубль кода. Завести новую карточку с причиной `already_landed` и доказательством.

Проверка: `git merge-base --is-ancestor 9179dd5111102e9b7c5502d8400b5c7e2f61c2bd origin/main` вернул 0. `gofmt -w` на трёх OwnerFiles прошёл без изменений. Полные проверки выполняет CI на PR.

Приёмка: Событие `event: "Stop"` и событие `hook_event_name: "Stop"` дают один и тот же переход в idle. Повтор с тем же pane и turn возвращает `AgentTurnDuplicate` и не создаёт второй переход. Событие чужого pane возвращает `AgentTurnForeignPane`. Невалидный JSON возвращает `ErrAgentTurnJSON`. Другие события сессии возвращают `AgentTurnNotStop`. Тест с `%1` в pane проходит. Все перечисленные тесты уже лежат в `internal/infra/observe/agent_turn_event_test.go` в main.

Причина: already_landed
Закрыл: ses_df8eb3689ffewpWdRZpiwcfcR5
Время закрытия: 2026-11-23T06:29:29Z

## Подтверждение

- PR #2133, коммит `9179dd5111102e9b7c5502d8400b5c7e2f61c2bd`, тема `feat(observe): CD-19 parse agent turn end events and derive idle transition (#2133)`.
- `git merge-base --is-ancestor 9179dd5111102e9b7c5502d8400b5c7e2f61c2bd origin/main` вернул 0 при базе `79dc16941bdbb5ff312f85e8602dda0e67be1a2a`.
- Тесты в main: `TestAgentTurnTrackerStopAppliesIdle`, `TestParseAgentTurnEventHookEventName`, `TestAgentTurnTrackerDeduplicatesReplay`, `TestAgentTurnTrackerRejectsForeignPane`, `TestAgentTurnTrackerIgnoresOtherEvents`, `TestParseAgentTurnEventRejectsBadInput`.
- `gofmt -w` на трёх OwnerFiles изменений не дал. Полные проверки выполняет CI на PR.
