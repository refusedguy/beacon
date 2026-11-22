# MEM-04. Подключить ArchiveRun, Checkpoint, FootprintCheck и реaper к циклу
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/daemon.go
Зависимости: AUTOSP-04, AUTOSP-11, TUI-03, TUI-04, PROTO-03
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать готовые механизмы частью ReconcilePass и согласовать адаптеры.
Контекст: Источник: docs/research/audit-02-memory.md, строки таблицы про ArchiveRun, Checkpoint, FootprintCheck и разрыв delivery. ReconcilePass определён в cmd/beacon/mcp.go:1921, вызов bridge.Refresh в :1941. Готовые функции не имеют вызовов вне тестов: store.ArchiveRun в internal/infra/store/archive.go:198, store.Checkpoint в internal/infra/store/db.go:248, server.FootprintCheck в internal/infra/server/metrics.go:406. Метрика уже создана в cmd/beacon/mcp.go:1546 как pollerMetrics. Замыкание prompt в :1722 получает delivery, но бросает его. Реaper приходит из карточки MEM-06. Owner files cmd/beacon/mcp.go закреплён за AUTOSP-04, TUI-04 и PROTO-03, поэтому MEM-04 ждёт все три карточки.
Шаги: В ReconcilePass вызвать cfg.db.ArchiveRun, затем cfg.db.Checkpoint и затем решение AUTOSP-11 о запуске, а при его отсутствии server.FootprintCheck с pollerMetrics, числом активных Watch и server.ProcessRSSBytes. Ошибки архива и checkpoint писать через observe.ActorError, цикл не останавливать. Передавать delivery и resume из PromptFunc в обновлённый Client.Prompt из MEM-01. Вызвать ReapStaleCards из MEM-06 один раз за проход. Сохранить вызовы в ветке --once в cmd/beacon/daemon.go, чтобы одиночный проход делал ту же работу.
Проверка: CI зелёный. В mcp.go есть вызовы ArchiveRun, Checkpoint и FootprintCheck. Замыкание prompt передаёт оба поля. Тест --once видит один проход архива и один checkpoint.
Приёмка: Цикл поддерживает архив и WAL и печатает предупреждение о RSS. Адаптеры MEM-01 и MEM-03 подключены без второго HTTP-порта.

Итог: Already landed in PR #1602 (commit bda51c0fde01ddc2346be95d7927deb5647cdd75). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1602 merged 2026-11-22T15:36:03+05:00; commit bda51c0fde01ddc2346be95d7927deb5647cdd75; git merge-base --is-ancestor bda51c0fde01ddc2346be95d7927deb5647cdd75 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1602 bda51c0f
Время закрытия: 2026-11-22T11:25:31Z
