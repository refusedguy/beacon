# AUD-12. Убрать глобальный tx-кэш и перевести daemon на индекс
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/store/backfill_registry.go, cmd/beacon/daemon.go, cmd/beacon/watch.go, cmd/beacon/agents.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Устранить глобальный tx-кэш, метрику без обнуления и отсутствие ArchiveRun, Checkpoint и TTL.
Контекст: Источник: аудит fix/c18-a17, P1.8. registryColumns в backfill_registry.go кэширует колонки по указателю sql.Tx и может вернуть устаревшую карту после ALTER TABLE. daemonPollCards в cmd/beacon/daemon.go каждые 30 с дёргает taskstore.LoadCards и бесконечно инкрементирует file_adapter_reads_total. ArchiveRun и Checkpoint не вызываются. Нет TTL для in_progress без PR/Watch. watch.go и agents.go запускают opencode api через c.run вместо HTTP-порта. baseline: 1 глобальный кэш, 1 метрика без обнуления, 0 вызовов ArchiveRun и Checkpoint.
Шаги: Перенести кэш колонок в локальную переменную. Перевести daemonPollCards на чтение через индекс без инкремента legacy-метрики FileAdapterRead. Подключить ArchiveRun и Checkpoint в цикл демона. Добавить TTL-проверку зависших in_progress карточек. Перевести watch и agents на HTTP-порт с честным fallback. Если TTL требует чужие OwnerFiles, завести отдельную карточку через beacon_defer_wiring.
Проверка: CI зелёный. Тест подтверждает отсутствие глобального tx-кэша, метрика file_adapter_reads_total не растёт на каждом цикле, есть вызовы ArchiveRun и Checkpoint и TTL-проверка.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.

Итог: Already landed in #1609 (2b0249afca20933f82f9618537c97c9406f77a76). No duplicate code needed.
Доказательство: PR #1609 commit 2b0249afca20933f82f9618537c97c9406f77a76 merge-base --is-ancestor 2b0249afca20933f82f9618537c97c9406f77a76 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1609 2b0249a
Время закрытия: 2026-11-22T11:29:08Z
