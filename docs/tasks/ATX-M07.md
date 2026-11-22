# ATX-M07. Расширить ключ идемпотентности закрытия карточки
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_task_close.go, internal/infra/mcp/tool_task_close_test.go
Зависимости: ATX-M01
Ссылка: docs/research/mcp-close-chain-gaps.md, finding M07
Агент: нет
Время: нет
Цель: Сделать повтор закрытия безопасным при изменении любого важного поля входа.
Контекст: Источник: docs/research/mcp-close-chain-gaps.md, finding M07. `closeRecord` хранит `status`, `reason`, `duplicateOf`, `agentID` и `closedAt` (`internal/infra/mcp/tool_task_close.go:156-164`). Повтор сравнивает только `Status` и `Reason` (`:237-250`). Повтор с другим `agentID` или другой целью дубликата возвращает `ok` и не меняет ledger. Baseline: 2 поля из 5 участвуют в сравнении при повторе.
Шаги: Включить `agentID` и `duplicateOf` в сравнение при повторе. Решить, что делать с изменившимся значением: вернуть конфликт или записать новый отчёт. Сохранить поведение повтора после сбоя между записью ledger и переименованием файла. Добавить тесты на повтор с каждым изменённым полем и на повтор после частичной записи.
Проверка: Тесты зелёные в CI. Повтор с теми же пятью значениями остаётся идемпотентным, повтор с любым другим значением не тихо перезаписывает ledger.
Приёмка: Ключ идемпотентности покрывает все поля решения о закрытии. Конфликт возвращает машиночитаемый код, а не только текст ошибки.

Причина: Already landed in PR #1650 (commit aab2f6ed82bea0adbdc4bc371b1e4731ed2d0c2a): fix(mcp): extend close idempotency key (ATX-M07). git merge-base --is-ancestor aab2f6ed8 origin/main exit 0.
Закрыл: ses_dfcd81a70ffe6MNjcwhRE5cexF
Время закрытия: 2026-11-22T12:09:10Z
