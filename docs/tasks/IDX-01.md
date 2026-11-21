# IDX-01. Схема SQLite индекса карточек и импорт
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/taskstore/index.go, internal/infra/taskstore/index_test.go
Зависимости: 
Ссылка: 
Агент: ses_e02a360ecffe7tcQflfKPsOoHV
Время: 2026-11-21T09:26:02Z
baseline: Замер 2026-11-21 по docs/tasks: `find -name '*.md' | wc -l` = 970 карточек; `du -cb` = 1 637 944 байт, 1,56 МиБ; токены «Зависимости» = 961, «Владелец файлов» = 1896. Плановое значение из Контекст (931 карточка, 950 зависимостей, 1761 владелец, 1,45 МиБ) снято раньше, корпус с тех пор вырос на 39 карточек.
Решение: OpenIndex(path) создаёт таблицы cards, deps, owners в state dir. RebuildDir(tasksDir) читает карточки через LoadCards и ParseCard и в одной транзакции DELETE+INSERT пересобирает индекс, повтор с тем же корпусом даёт те же строки. ImportCards принимает готовый срез Card. Counts и raw_sha256 нужны внешним проверкам. Файл индекса называется card-index-<Hash8(root)>.sqlite.
Граница: Markdown остаётся источником истины. ParseCard, LoadCards и существующие команды не изменены. Индекс лежит в state dir и в репозиторий не попадает.
Ограничение: Каждый импорт полностью перезаписывает индекс. Подключение сборки к CLI и CI и инкрементальное обновление оставлены карточкам IDX-03 и IDX-04.
Цель: Пересобираемый индекс карточек в SQLite с таблицами cards, deps, owners.
Контекст: Premise: markdown остаётся источником истины, SQLite в state dir становится индексом. baseline: 931 карточка, 1,45 МиБ, 950 dependency-токенов, 1761 owner-токен.
Шаги: Спроектировать схему, импортировать все карточки через ParseCard, идемпотентный повторный импорт, тесты на 931 запись.
Проверка: gofmt, go test в CI.
Приёмка: Импорт 931 карточки и 950 зависимостей без ошибок, повтор не меняет данные.

## Подтверждение

Проверено 2026-11-21 по `git show origin/main:internal/infra/taskstore/index.go` и `gh pr view 912`. Реализация уже влита в origin/main через PR #912, https://github.com/refusedguy/beacon/pull/912. Merge commit b4dc900c31f4a44e8bd873275b769af89b6a6f48 добавил `internal/infra/taskstore/index.go` и `internal/infra/taskstore/index_test.go`. Схема `indexSchemaSQL` создаёт таблицы `cards`, `deps`, `owners`; `OpenIndex` применяет её идемпотентно, `ImportCards` и `RebuildDir` пересобирают индекс в одной транзакции DELETE+INSERT. Тесты `TestImportCardsWritesRows`, `TestImportCardsIdempotent` и `TestImportCardsDeduplicatesTokens` фиксируют поведение. Повторной реализации не делалось, чтобы не дублировать влитый код. Статус переведён в done.
