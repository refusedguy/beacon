# CCx-24. Классификация отказа песочницы при записи patch
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, internal/infra/batchapply/denial_test.go
Зависимости: CCx-23
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/runtimes/apply_patch.rs:256-272
Агент: ses_ccx24c_executor
Время: 2026-11-22T18:24:17Z
Цель: Отличать отказ песочницы от обычной ошибки записи и отправлять отдельную метрику.
Контекст: Источник: docs/research/comp-codex.md. Codex после применения patch проверяет stdout и stderr на признаки отказа песочницы (`codex-rs/core/src/tools/runtimes/apply_patch.rs:256-261`). При отказе он пишет метрику нарушения и возвращает `SandboxErr::Denied` вместе с выводом (`:262-272`). Beacon возвращает из `writeFileMode` голую ошибку `os.WriteFile` (`internal/infra/batchapply/apply.go:611`). Классификации и метрики нарушения нет. Baseline: 0 классификаций отказа, 0 метрик нарушения в пакетной записи.
Шаги: Добавить тип ошибки отказа песочницы с полем вывода. Классифицировать ошибку по выходным данным writer. Записывать метрику нарушения только для этого типа. Покрыть тестом на отказ и на обычную ошибку прав доступа.
Проверка: CI зелёный. Тест с отказом показывает 1 классифицированную ошибку и 1 метрику нарушения. Обычная ошибка прав доступа остаётся неклассифицированной и метрику не пишет.
Приёмка: Изменения только в OwnerFiles карточки. Формат сводки CLI менять только добавлением нового поля.

Причина: Already landed in PR #1909 (merge commit 7d810af7, "feat(batchapply): classify sandbox write denials (CCx-24)"); git merge-base --is-ancestor 7d810af7 origin/main exit 0. Implementation is present in internal/infra/batchapply/apply.go and denial_test.go. No duplicate code written.
Закрыл: ses_ccx24c_executor
Время закрытия: 2026-11-22T18:25:13Z