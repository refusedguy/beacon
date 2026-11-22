# ATX-V03. Убрать информационные проверки из wake keep_working и retry
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/wake_dispatcher.go, internal/infra/cibridge/wake_informational_test.go
Зависимости: ATX-V01
Ссылка: docs/research/verdict-contract-gaps.md, раздел 3, docs/templates/executor-task.md:199-211
Агент: нет
Время: нет
Цель: Сообщать исполнителю только те проверки, которые он обязан починить или повторить.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 3. `wakeText` в wake_dispatcher.go:807-845 берёт `failed` от `realFailedNames`. Ветка `keep_working` (:811-819) пишет «чини конкретные failedChecks» со всеми именами. Ветка `retry` (:827-839) подставляет тот же список в `gh run rerun --failed`, когда `RerunChecks` пуст. Baseline: одно неотфильтрованное имя в wake способно сбить executor с требуемого действия.
Шаги: Использовать канонический список имён из ATX-V01 для текста `keep_working`. Для `retry` брать `RerunChecks` из wake target, а при пустом списке использовать только блокирующие имена. Не менять формат самого wake и маркер main head.
Проверка: CI зелёный. Табличный тест с required `ci-build` и optional `ci-info`: текст `keep_working` не содержит `ci-info`, текст `retry` не содержит `ci-info` в команде rerun. Тест только с optional FAILURE не создаёт wake `keep_working`.
Приёмка: Исполнитель не получает указания чинить или повторять informational-проверку.

Итог: Already landed in PR #1643 (commit cb70a45df173655a27dddd9146a966fb21031eaa): fix(cibridge): hide informational checks in keep_working and retry wake (ATX-V03)
Доказательство: PR #1643; commit cb70a45df173655a27dddd9146a966fb21031eaa; git merge-base --is-ancestor cb70a45df173655a27dddd9146a966fb21031eaa origin/main exit 0; wakeText now calls blockingFailedNames, which drops failed checks with Required: false from keep_working and retry wake text.
Причина: already landed in PR #1643 cb70a45df173
Время закрытия: 2026-11-22T11:53:03Z
