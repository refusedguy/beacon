# ATX-V02. Информационная проверка не попадает в BlockedInfo и инцидент
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/verdict_applier.go, internal/infra/cibridge/blocked_informational_test.go
Зависимости: ATX-V01
Ссылка: PR #1279, docs/research/verdict-contract-gaps.md, раздел 2
Агент: нет
Время: нет
Цель: Не давать informational-проверке менять сигнатуру BlockerIncident и текст wake FIX-агента.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 2. `applyBlocked` в verdict_applier.go:557-597 зовёт `realFailedNames` и кладёт результат в `BlockedInfo.FailedChecks` и в `GetOrCreateOpenBlocker`. `blockedNotice` (:603-610) печатает тот же список оркестратору. `handleBlocked` (:744-775) повторяет операцию на каждом цикле. Baseline: после PR #1279 правило 5 пропускает optional base failure, но список имён остался неотфильтрованным.
Шаги: Передавать в `GetOrCreateOpenBlocker` только блокирующие имена из ATX-V01. Оставить informational-имена в полях диагностики Watch, если они нужны для чтения, но не в подписи инцидента. Убрать их из `blockedNotice`. Проверить, что два Watch с одним required-сбоем и разными optional-сбоями попадают в один инцидент.
Проверка: CI зелёный. Тест создаёт наблюдение с required `ci-build` и optional `ci-info`, обе в Failures. `BlockedInfo.FailedChecks` содержит только `ci-build`. Ключ инцидента не меняется при подмене `ci-info` на `ci-other`.
Приёмка: FIX-агент получает в wake только проверки, которые действительно блокируют Watch.
