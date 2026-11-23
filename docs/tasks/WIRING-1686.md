# WIRING-1686. Подключить CheckResume к восстановлению работы
Статус: done
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/attach.go
OwnerFiles: см. поле «Владелец файлов»
Зависимости: PR 1686
Ссылка: PR 1686
SourceWatch: watch:refusedguy/beacon:1686:839072d85f55ed49cbfe4fa73aa0bb83cafc1c2b@1795351987:1800
PR: 1686
Агент: ses_wiring1686_executor
Время: 2026-11-22T18:25:13Z
Цель: Подключить отложенную часть фичи после мержа PR 1686.

Контекст: PR 1686 добавляет `CheckResume` и `ResumeFacts` в `internal/infra/worktree/resume_guard.go`. Проверка не вызывает git и ждёт готовые факты. Вызывающий код живёт в `cmd/beacon/attach.go` и не входит в OwnerFiles карточки CD-02.

Шаги: Дождаться мержа PR 1686; собрать в `cmd/beacon/attach.go` имя ветки, путь worktree, флаг занятости другим worktree и счётчики ahead и behind из `git rev-list --left-right --count`; передать факты в `CheckResume` и действовать по решению `open`, `reuse` или `refuse`; вызвать `beacon deferred done` для записи с watchID из SourceWatch.

Проверка: Карточка `docs/tasks/WIRING-1686.md` попала в PR; локальный гейт `WIRING-*` проходит: статус `todo`, OwnerFiles заполнен, `## Причина` есть.

Приёмка: Проводка применена, запись в `beacon deferred --format json` имеет `status: done`, CI зелёный.

Причина: already_landed
Закрыл: beacon
Время закрытия: 2026-11-22T18:30:43Z
## Причина

Файл `cmd/beacon/attach.go` принадлежит другой карточке. CD-02 не меняет чужой OwnerFiles и передаёт подключение владельцу файла через отдельную задачу.

## Подтверждение
- PR #2147 MERGED: 2026-11-22 (squash).
