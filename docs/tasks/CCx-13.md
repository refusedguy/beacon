# CCx-13. Отбор кандидата сессии из базы состояния
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/attach.go, cmd/beacon/attach_candidate_test.go
Зависимости: CCx-10
Ссылка: docs/research/comp-codex.md, codex-rs/exec/tests/suite/resume.rs:297-359, :361-416
Агент: ses_ccx13d_executor
Время: 2026-11-22T18:09:54Z
Цель: Использовать кандидата из базы только после проверки, что он годится для запрошенной сессии.
Контекст: Источник: docs/research/comp-codex.md. Codex отдельно проверяет два случая. Пригодный кандидат из базы состояния принимается (`codex-rs/exec/tests/suite/resume.rs:297-359`). Кандидат с несовпадающими полями пропускается (`:361-416`). Beacon в `registryResumeSession` берёт любую строку, у которой непустое поле `ID` (`cmd/beacon/attach.go:183-195`). Дополнительной проверки нет: подменённая запись меняет sessionID, который затем уходит в `opencode`. Baseline: 0 проверок пригодности кандидата в `cmd/beacon/attach.go:183-195`.
Шаги: Определить признаки пригодной записи: id, задача и время последнего события. Сверять их с запрошенной сессией. При несовпадении возвращать исходный id из `attach-sessions.json`. Покрыть двумя тестами на пригодного и несовпадающего кандидата.
Проверка: CI зелёный. Тест с пригодным кандидатом открывает 1 сессию с id из базы. Тест с несовпадающим кандидатом открывает 1 сессию с id из файла.
Приёмка: Изменения только в OwnerFiles карточки. Порядок чтения registry и `attach-sessions.json` сохранить.

Причина: CCx-13 уже реализован коммитом 552d9e0d (PR #1873) и влит в origin/main. merge-base --is-ancestor 552d9e0d origin/main вернул 0. В cmd/beacon/attach.go есть attachCandidateFits, savedAttachRecord и проверка пригодности кандидата в registryResumeSession. Два теста лежат в cmd/beacon/attach_candidate_test.go. Дубль кода не нужен.
Закрыл: ses_ccx13d_executor
Время закрытия: 2026-11-22T18:10:38Z
## Подтверждение
- PR #2087 MERGED: 2026-11-22 (squash).

