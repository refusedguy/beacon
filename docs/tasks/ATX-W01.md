# ATX-W01. Не удалять обязательства с запланированным повтором при GC
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/store/gc.go, internal/infra/store/gc_test.go
Зависимости: 
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W01
Агент: нет
Время: нет
Цель: Сохранить обязательство с живым ретраем при чистке обязательств закрытых Watch.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Прерванный ретрай при GC». PR #1275 добавил `GCObligations`, но `obligationLastActivity` в `internal/infra/store/gc.go:151-161` смотрит только `StagedAt`, `DeliveredAt`, `ClaimedAt`, `ReadAt` и `EscalatedAt`. Поля `NextRetryAt` и `Attempts[].At` не участвуют. Для обязательства без строки Watch ветка `missing` берёт эту функцию как опорное время и удаляет строку старше 24 часов даже при отложенном повторе. Baseline: тест PR #1275 в `gc_test.go` покрывает только старый `StagedAt` без ретрая.
Шаги: Включить `NextRetryAt` и максимальный `Attempts[].At` в расчёт последней активности. Считать обязательство молодым, если `NextRetryAt` больше текущего времени. Запретить удаление при ненулевом будущем `NextRetryAt` независимо от ретеншна. Добавить тест с отсутствующей строкой Watch, старым `StagedAt` и будущим `NextRetryAt`.
Проверка: CI зелёный. Повторный прогон `GCObligations` с тем же `now` не удаляет строку с активным ретраем. Строка без ретрая и со старым временем по-прежнему удаляется.
Приёмка: Живой ретрай переживает GC, аудит удаления не содержит обязательств с отложенной доставкой.

Причина: Already landed in PR #1642 (commit 4bfa7802508ff04254c6527dcdda4dda5cad8747): fix(store): keep obligations with a pending retry through GC (ATX-W01). git merge-base --is-ancestor 4bfa78025 origin/main exit 0.
Закрыл: ses_dfcd81a70ffe6MNjcwhRE5cexF
Время закрытия: 2026-11-22T12:09:10Z
