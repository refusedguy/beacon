# AUD-05. Свести три файловых лока и формат закрытия к одному контракту
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/taskstore/close.go, internal/infra/taskstore/store.go, internal/infra/mcp/tool_task_claim.go, internal/infra/mcp/tool_task_close.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Использовать один лок и один формат закрытия для всех операций с карточками.
Контекст: Источник: аудит fix/c18-a17, P0.4. taskstore.AcquireLock держит task-<hash>.lock и CloseCard пишет шесть полей. FileTaskClaimStore использует .beacon-task-claim.lock, FileTaskCloseStore — .beacon-task-close.lock и sidecar .beacon-task-close.json, меняя в .md только Статус. baseline: 3 разных лока, 2 разных формата закрытия.
Шаги: Ввести один лок на каталог docs/tasks, который используют taskstore, claim и close. Синхронизировать поля закрытия FileTaskCloseStore с taskstore/close.go. Убрать или опубликовать sidecar так, чтобы он не расходился с .md. Сохранить идемпотентность close по cardID+status+reason.
Проверка: CI зелёный. Тесты подтверждают один лок на все три операции и совпадение полей .md с taskstore.CloseCard. Повтор close с тем же ключом остаётся ok=true.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.

Итог: Already landed in #1469 (0efa2344ba84ced85d9dae11d63947613259818c). No duplicate code needed.
Доказательство: PR #1469 commit 0efa2344ba84ced85d9dae11d63947613259818c merge-base --is-ancestor 0efa2344ba84ced85d9dae11d63947613259818c origin/main exit 0; no open duplicate PR.
Причина: already landed in #1469 0efa234
Время закрытия: 2026-11-22T11:29:08Z
