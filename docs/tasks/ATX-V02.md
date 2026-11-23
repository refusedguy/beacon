# ATX-V02. Информационная проверка не попадает в BlockedInfo и инцидент
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/verdict_applier.go, internal/infra/cibridge/blocked_informational_test.go
Зависимости: ATX-V01
Ссылка: PR #1279, docs/research/verdict-contract-gaps.md, раздел 2
Агент: ses_atxv02_executor
Время: 2026-11-22T16:55:07Z
Цель: Не давать informational-проверке менять сигнатуру BlockerIncident и текст wake FIX-агента.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 2. `applyBlocked` в verdict_applier.go:557-597 зовёт `realFailedNames` и кладёт результат в `BlockedInfo.FailedChecks` и в `GetOrCreateOpenBlocker`. `blockedNotice` (:603-610) печатает тот же список оркестратору. `handleBlocked` (:744-775) повторяет операцию на каждом цикле. Baseline: после PR #1279 правило 5 пропускает optional base failure, но список имён остался неотфильтрованным.
Шаги: Передавать в `GetOrCreateOpenBlocker` только блокирующие имена из ATX-V01. Оставить informational-имена в полях диагностики Watch, если они нужны для чтения, но не в подписи инцидента. Убрать их из `blockedNotice`. Проверить, что два Watch с одним required-сбоем и разными optional-сбоями попадают в один инцидент.
Проверка: CI зелёный. Тест создаёт наблюдение с required `ci-build` и optional `ci-info`, обе в Failures. `BlockedInfo.FailedChecks` содержит только `ci-build`. Ключ инцидента не меняется при подмене `ci-info` на `ci-other`.
Приёмка: FIX-агент получает в wake только проверки, которые действительно блокируют Watch.

Причина: Уже влито в origin/main через PR #1646, коммит 7debcb0d (fix(cibridge): keep informational checks out of blocker incidents (ATX-V02)). git merge-base --is-ancestor 7debcb0d origin/main -> exit 0. Дубль кода не нужен.
Закрыл: ses_atxv02_executor
Время закрытия: 2026-11-22T16:55:14Z
## Подтверждение
- PR #1976 MERGED: 2026-11-22 (squash).

