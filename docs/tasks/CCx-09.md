# CCx-09. Reverse scan до compaction cutoff при fork
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/attach.go, internal/app/session/registry.go, cmd/beacon/attach_fork_test.go
Зависимости: CCx-10
Ссылка: docs/research/comp-codex.md, codex-rs/thread-store/src/local/model_context.rs:27-34, :76-87, :97-110
Агент: ses_ccx09c_executor
Время: 2026-11-22T18:01:07Z
Цель: Не тащить всю историю сессии в fork, если в журнале есть пригодная compaction.
Контекст: Источник: docs/research/comp-codex.md. Codex сканирует журнал с конца и останавливается на самой новой `CompactedItem` с replacement history и window number (`codex-rs/thread-store/src/local/model_context.rs:27-34`). Для paginated журнала он читает lineage с fallback (`:76-87`), а fork берёт замороженный префикс (`:97-110`). Beacon при рестарте читает `git status` и ключ `beacon/survival-rules` (`AGENTS.md:103-111`). `attach --resume` выбирает сохранённый id в `cmd/beacon/attach.go:225-241`, а registry возвращает строку по id (`internal/app/session/registry.go:132-140`). Cursor compaction отсутствует. Baseline: 0 compaction cutoff и 0 reverse scan в пути возобновления.
Шаги: Записать cutoff compaction в метаданные сессии. При fork читать журнал с конца до cutoff. Если самая новая запись неполная, продолжать сканирование до начала. Покрыть тестом на compaction в середине журнала.
Проверка: CI зелёный. Тест с compaction в середине и новым хвостом возвращает 1 compaction и только записи хвоста. Тест с неполной compaction читает всю историю.
Приёмка: Изменения только в OwnerFiles карточки. Формат `attach --resume` и имена флагов не менять.

Причина: Already landed in PR #1826: commit f1ad10f6 feat(session): reverse scan to compaction cutoff at fork (CCx-09), merge-base --is-ancestor f1ad10f6 origin/main exit 0. Implementation in cmd/beacon/attach.go, internal/app/session/registry.go, cmd/beacon/attach_fork_test.go; no duplicate code. Closing card status to match main.
Закрыл: ses_ccx09c_executor
Время закрытия: 2026-11-22T18:01:32Z
## Подтверждение
- PR #2068 MERGED: 2026-11-22 (squash).

