# ATX-43. Вызывать FixDelivered в продакшене
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/observe/observe.go, internal/infra/wake/deliver.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать fix_total ненулевым при реальной доставке FIX-агенту.
Контекст: Источник: docs/research/metrics-audit.md, строка таблицы 116 и раздел «Почему fix_total всегда нулевой». internal/infra/observe/observe.go:458, FixDelivered инкрементирует fix_total{role=fixer}. Единственный вызов хелпера в observe_test.go:84. В продакшене вызова нет. Карточка docs/tasks/T5.67.md:2 отложила точку вызова в wake/deliver.go до ADR 0260. Baseline: счётчик всегда 0, событие fix_delivered не возникает.
Шаги: Добавить вызов FixDelivered в точке доставки FIX-агенту в wake/deliver.go. Добавить тест на инкремент. Обновить runbook observability.md, если он описывает метрику.
Проверка: CI зелёный. Интеграционный тест доставки FIX-агенту видит fix_total{role=fixer} больше нуля.
Приёмка: Счётчик растёт только при реальной доставке. Событие fix_delivered появляется в рабочем процессе.

Итог: Cardindex resolves done: OwnerFiles overlap with T3.417 (already done). Supporting merged commit ca9763c2dd79 (PR #1496) touches the card OwnerFiles.
Доказательство: Commit ca9763c2dd79d19184a882fb053f88ea3e67caa0 (PR #1496, fix(in5-02): pass delivery and message id into HTTP wake prompts (#1496)) touches OwnerFiles internal/infra/observe/observe.go, internal/infra/wake/deliver.go; git merge-base --is-ancestor ca9763c2dd79d19184a882fb053f88ea3e67caa0 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1496 ca9763c2dd79
Время закрытия: 2026-11-22T11:27:01Z
