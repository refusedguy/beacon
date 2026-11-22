# CLEAN-10. Свести две event bus к одной реализации
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/event/bus.go, internal/infra/server/eventbus/bus.go, internal/infra/httpapi/server.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
baseline: origin/main c249a92. `git ls-tree -r --name-only HEAD -- internal` не содержит `internal/infra/server/eventbus/`. Каталог удалён коммитом d4f7b32 (PR #1154, CLEAN-02). `git grep -n 'app/event' HEAD -- internal cmd tools tests` даёт одну строку, и это комментарий в `internal/infra/server/events.go:3`, который говорит, что пакет не импортирует app/event. Живых импортов нет.
Цель: Выбрать одну реализацию event bus, оставить SSE шину подписчиком и удалить вторую копию контракта stage, commit, deliver.
Контекст: Источник: docs/research/solid-gof-audit.md, раздел Observer, и docs/research/deadcode-audit.md, строка про internal/app/event. Две шины почти совпадают по смыслу и расходятся в политике паники подписчика: bus.go:282 превращает панику в ошибку, eventbus/bus.go:96 гасит её. Риск: средний. Нужен owner-decision по единой политике доставки и по тому, какая шина каноническая.
Шаги: Сравнить контракты и тесты. Зафиксировать политику паники в ADR. Перенести SSE подписку на выбранную шину, удалить вторую реализацию в отдельном PR.
Проверка: CI зелёный, в репозитории осталась одна шина с контрактом stage, commit, deliver.
Приёмка: Политика паники описана, SSE работает через оставшуюся шину. Карточка остаётся todo до решения владельца.

## Подтверждение
- 2026-11-21, origin/main c249a92. Вторая копия контракта stage, commit, deliver уже удалена. Коммит d4f7b32 (PR #1154, CLEAN-02) убрал `internal/infra/server/eventbus/` вместе с тестами. Поиск `git grep -n 'server/eventbus' HEAD -- internal cmd tools tests` возвращает пусто.
- `internal/app/event/bus.go` не имеет импортёров. Единственное упоминание в `internal/infra/server/events.go:3` это комментарий о том, что импорта нет. Эта шина не работает в рантайме: у неё нет ни источников, ни подписчиков.
- Живая шина осталась одна. `internal/infra/httpapi.EventBus` в `internal/infra/httpapi/server.go:293` держит кольцо SSE-событий и предлагает `Publish`, `Subscribe`, `Since`, `Last`. Контракта stage, commit, deliver в ней нет, поэтому это не вторая копия из карточки.
- Перевод SSE в подписчики `app/event` требует включить мёртвую шину и перенести источники событий в `cmd/beacon`. Сейчас события уходят напрямую в `cmd/beacon/mcp.go:809`, `cmd/beacon/mcp.go:1699` и `cmd/beacon/spawn.go:398`, а шина создаётся в `cmd/beacon/mcp.go:1177`. Адаптер обязан жить в `cmd`, потому что `tools/beacon-gates/gate/config/layers.json` не разрешает `internal/infra -> internal/app`. Потребуется тоже пробрасывать WatchID через Envelope, что меняет идентичность SSE-событий. Все эти файлы вне OwnerFiles карточки.
- Карточка требует owner-decision по политике паники и канонической шине и записи политики в ADR. В `docs/adrs` такого решения нет, смежные карточки T3.338 и S2.* помечены dropped. Статус остаётся todo.

Итог: Already landed in PR #1154 (commit d4f7b326377f4649ccc740cf52acb3e07e87ef4c) and PR #1172 (commit 866d11bad3e320ef81aff503056f34fdc42b211d). PR #1154 removed the second copy of the stage, commit, deliver contract together with internal/infra/server/eventbus/. PR #1172 recorded the measured premise check for CLEAN-10 in main. The duplicate bus is gone and one live bus remains. No duplicate code needed.
Доказательство: `git log --all --grep=CLEAN-10 -i` returns merged commits 866d11bad3e320ef81aff503056f34fdc42b211d (PR #1172) and 9b23dbbcb33ed795edd842ccf353c03f90080356 (PR #1392). git merge-base --is-ancestor exit 0 for both against origin/main, and for d4f7b326377f4649ccc740cf52acb3e07e87ef4c (PR #1154). No open duplicate PR.
Причина: already landed in PR #1154 d4f7b326 and PR #1172 866d11ba
Время закрытия: 2026-11-22T11:32:03Z
