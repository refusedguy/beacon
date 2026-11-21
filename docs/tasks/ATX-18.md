# ATX-18. Снимать snapshot перед внешними вызовами в card_monitor.Poll
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/tasks/card_monitor.go
Зависимости: 
Ссылка: 
Агент: exe98
Время: 2026-11-21T17:54:02Z
Цель: Убрать deadlock при реентерабельности порта монитора карточек.
Контекст: Источник: docs/research/concurrency-audit.md, раздел 7 и строка таблицы 137. internal/app/tasks/card_monitor.go:155 держит m.mu весь Poll, включая вызовы m.port.Card в :171 и m.port.EventsAfter в :175. Пока идёт сетевой вызов, блокируются Subscribe, Unsubscribe, Subscribed и SubscriptionCount в :115-148. Порт, который дотронется до того же Monitor, вызывает deadlock. Приоритет средний в отчёте.
Шаги: Снять snapshot под блокировкой, отпустить её, вызвать порты, затем снова взять и сверить версию состояния.
Проверка: CI зелёный. Тест с портом, чей Card вызывает m.Subscribed(), завершается. Замер времени Subscribe при заблокированном Card не показывает ожидание мьютекса.
Приёмка: Внешние вызовы идут без удержания m.mu. Состояние перепроверяется по версии после вызова.

Итог: Fix already landed in main via PR #1230 (commit a77514e61b170074c20498ab78b88b6eb777533b), no duplicate code PR needed
Доказательство: git merge-base --is-ancestor a77514e61b170074c20498ab78b88b6eb777533b origin/main exits 0; PR #1230 MERGED 2026-11-21T16:35:27Z; https://github.com/refusedguy/beacon/pull/1230
Время закрытия: 2026-11-21T17:54:02Z
