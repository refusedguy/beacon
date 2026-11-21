# BCX-01. Исключить docs-only recheck из сигнала implementation merged
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: tools/roadmap-sync/main.go, docs/tasks/T5.52.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Исправить признак merged в roadmap-sync: recheck коммиты с префиксом docs( не должны переводить карточку в done.
Контекст: Источник: docs/research/docs-health-audit.md, находка 1 и таблица 7 строка 1. tools/roadmap-sync/main.go:212 собирает ID из subject с (#NNNN), :230 повышает статус, :301 распространяет ошибку. PR #1000 меняет только docs/tasks, но даёт T5.52 todo -> done; настоящая реализация 13f8558c (#1107) не на main. Базовый замер и приоритет из отчёта: P0.
Шаги: Ввести четыре условия: subject с (#NNNN), ID карточки, изменение OwnerFiles вне docs/, исключение recheck-префиксов docs(, premise rechecked, baseline refreshed, keep todo, refresh. Добавить поле Impl-PR в карточку. Запретить resolveDuplicateCards повышать todo по чуждому OwnerFiles.
Проверка: CI зелёный. Dry run roadmap-sync на main не переводит T5.52 в done из-за #1000. Тест на docs-only коммит и на recheck с изменённым кодом.
Приёмка: T5.52 остаётся todo до влития #1107. Recheck-коммиты пишут Premise и baseline, но не меняют Статус и Impl-PR.
