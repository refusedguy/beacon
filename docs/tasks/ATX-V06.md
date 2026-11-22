# ATX-V06. Легаси-провайдер не делает informational-проверку обязательной
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/ghci/provider.go, internal/infra/ghci/provider_test.go, internal/infra/github/batch_provider.go
Зависимости: ATX-V01
Ссылка: docs/research/verdict-contract-gaps.md, раздел 6
Агент: нет
Время: нет
Цель: Не проставлять `Required: true` проверкам, обязательность которых неизвестна.
Контекст: Источник: docs/research/verdict-contract-gaps.md, раздел 6. `PRState` в ghci/provider.go:283-289 ставит `Required: true` каждой строке из `gh pr checks`. Проверка с `continue-on-error: true` в workflow становится блокирующей для вердикта. Батч-провайдер в batch_provider.go:577 читает `IsRequired`, что точнее, но тоже не знает про `continue-on-error`. Поиск по `internal` и `cmd` не нашёл ни одного чтения поля `continue-on-error` из workflow. Baseline: один провайдер с константой `true` и ноль чтений поля `continue-on-error`.
Шаги: Заполнять `Required` из доступного признака обязательности. Когда признака нет, оставлять значение неизвестным и не давать verdict `ready` на таких фактах. Добавить тест, где optional FAILURE из легаси-провайдера не даёт `keep_working` и не будит executor.
Проверка: CI зелёный. Тест провайдера проверяет, что строка `gh pr checks` без данных об обязательности не помечается как required. Тест снимка подтверждает отсутствие `keep_working`.
Приёмка: Ни один провайдер не превращает informational-проверку в обязательную подстановкой константы.

Итог: Cardindex resolves done: OwnerFiles overlap with T3.175 (already done). Supporting merged commit 3137a74c601d (PR #1415) touches the card OwnerFiles.
Доказательство: Commit 3137a74c601d55e8634efd99274749b5f1ab26d3 (PR #1415, refactor(verdict): share one PASS/FAIL vocabulary across adapters (#1415)) touches OwnerFiles internal/infra/ghci/provider.go, internal/infra/ghci/provider_test.go, internal/infra/github/batch_provider.go; git merge-base --is-ancestor 3137a74c601d55e8634efd99274749b5f1ab26d3 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1415 3137a74c601d
Время закрытия: 2026-11-22T11:27:01Z
