# BCX-08. Исправить шесть сломанных ссылок в FACTS.md
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, docs/FACTS.md
Зависимости: 
Ссылка: 
Агент: exe128
Время: 2026-11-21T18:02:32Z
Цель: Генерировать корректные относительные ссылки для владельцев вне каталога docs.
Контекст: Источник: docs/research/docs-health-audit.md, раздел 2.2 и таблица 7 строка 8. internal/infra/batchapply/apply.go:254 делает strings.TrimPrefix(owner, "docs/"). Ссылки README.md и AGENTS.md (:45, :167), cmd/beacon/observe.go (:80), evals/main.go (:172, :178, :181) указывают в несуществующий docs/. Базовый замер и приоритет из отчёта: P2.
Шаги: В renderFacts добавлять ../ для owner без префикса docs/. Перегенерировать FACTS.md. Добавить проверку разрешения локальных ссылок на этапе генерации.
Проверка: CI зелёный. Все шесть ссылок разрешаются из каталога docs. Тест renderFacts покрывает owner внутри и вне docs.
Приёмка: Ни одна строка FACTS.md не ведёт в несуществующий файл. Генератор сам отбраковывает неразрешимую ссылку.

Итог: Already landed in PR #1262 (commit e427c690a0a324a324fcb6b7fd49f09f9761502f): factsLink adds ../ for owners outside docs; six broken links resolved
Доказательство: PR #1262 merged 2026-11-21T16:52:27Z; commit e427c690a0a324a324fcb6b7fd49f09f9761502f changes internal/infra/batchapply/apply.go; git merge-base --is-ancestor e427c690a0a324a324fcb6b7fd49f09f9761502f origin/main exit 0; docs/FACTS.md derived and gitignored per IDX-11; no open duplicate PR
Причина: already landed in PR #1262 e427c690
Время закрытия: 2026-11-21T18:07:35Z
