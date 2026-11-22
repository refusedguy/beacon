# TUI-06. Провести ребро матрицы и пересобрать гейт после решения владельца
Статус: done
Фаза: P0
Тип: task
Владелец файлов: tools/beacon-gates/gate/config/layers.json, tools/beacon-gates/gate/gate_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Сделать импорт internal/tui из тонкого адаптера легальным для обоих проходов layer-гейта.
Контекст: Источник: docs/research/audit-03-tui.md, раздел про матрицу слоёв. tools/beacon-gates/gate/config/layers.json:15 не содержит ребра cmd к internal/tui. gate/layers.go:78 требует убывания ранга, gate/layers.go:398 поднимает LAYER-UNUSED-RULE для неиспользуемого ребра. gate/gate.go:20 встраивает конфиг в бинарник. docs/tasks/WIRING-915.md:46 ждёт решения владельца. PR #1111 уже добавляет ребро, фикстуру и пересобранный бинарник.
Шаги: Дождаться ответа владельца на WIRING-915 и сверить PR #1111. Если ребро уже влито, только проверить фикстуру на реальное использование. Иначе добавить одно ребро cmd к internal/tui, добавить синтетический импорт в gate/fixtures.go и пересобрать tools/beacon-gates/beacon-gates по .github/workflows/gates-binary.yml. Не запускать beacon gates локально. Проверку оставить CI.
Проверка: CI зелёный по layers-ast, layers-list и сравнению закоммиченного бинарника со сборкой. LAYER-UNUSED-RULE не поднят. Статус WIRING-915 обновлён при мерже.
Приёмка: Матрица, фикстура и бинарник описывают одно состояние. Ребро используется реальным адаптером.

Итог: Already landed in PR #1111 (commit 9becefcca0086917650d757b0151fc33aa4c0bbd): feat(board): open interactive TUI from beacon board dispatch
Доказательство: PR #1111; commit 9becefcca0086917650d757b0151fc33aa4c0bbd; git merge-base --is-ancestor 9becefcca0086917650d757b0151fc33aa4c0bbd origin/main exit 0; layers.json carries the cmd -> internal/tui edge, fixtures.go adds the synthetic import, the embedded beacon-gates binary was rebuilt in the same PR and WIRING-915 was updated.
Причина: already landed in PR #1111 9becefcca008
Время закрытия: 2026-11-22T11:47:55Z
