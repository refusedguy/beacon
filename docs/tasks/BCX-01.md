# BCX-01. Исключить docs-only recheck из сигнала implementation merged
Статус: done
Фаза: P0
Тип: task
Владелец файлов: tools/roadmap-sync/main.go, docs/tasks/T5.52.md
Зависимости: 
Ссылка: 
Агент: exe120
Время: 2026-11-21T17:58:47Z
Цель: Исправить признак merged в roadmap-sync: recheck коммиты с префиксом docs( не должны переводить карточку в done.
Контекст: Источник: docs/research/docs-health-audit.md, находка 1 и таблица 7 строка 1. tools/roadmap-sync/main.go:212 собирает ID из subject с (#NNNN), :230 повышает статус, :301 распространяет ошибку. PR #1000 меняет только docs/tasks, но даёт T5.52 todo -> done; настоящая реализация 13f8558c (#1107) не на main. Базовый замер и приоритет из отчёта: P0.
Шаги: Ввести четыре условия: subject с (#NNNN), ID карточки, изменение OwnerFiles вне docs/, исключение recheck-префиксов docs(, premise rechecked, baseline refreshed, keep todo, refresh. Добавить поле Impl-PR в карточку. Запретить resolveDuplicateCards повышать todo по чуждому OwnerFiles.
Проверка: CI зелёный. Dry run roadmap-sync на main не переводит T5.52 в done из-за #1000. Тест на docs-only коммит и на recheck с изменённым кодом.
Приёмка: T5.52 остаётся todo до влития #1107. Recheck-коммиты пишут Premise и baseline, но не меняют Статус и Impl-PR.

## Подтверждение
Исправление уже лежит в `origin/main` через PR #1266 `fix(roadmap-sync): exclude docs-only rechecks from merged signal`, ветка `fix/bcx01`. Полный SHA коммита: `2d5e87de518d58879b87c75e055f7613b352eef0`. Проверка `git merge-base --is-ancestor 2d5e87de518d58879b87c75e055f7613b352eef0 origin/main` вернула код 0. Коммит меняет `tools/roadmap-sync/main.go` и `docs/tasks/T5.52.md`: добавлены `isRecheckSubject` с префиксом `docs(` и маркерами `premise rechecked`, `baseline refreshed`, `keep todo`, `refresh`, проверка изменения OwnerFiles вне `docs/` и поле `Impl-PR`. Дубль кода и второй код-PR не создавались. Т5.52 теперь `done` с `Impl-PR: #1107`.
