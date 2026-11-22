# FTE-04. Завести wiring-карточки для четырёх отложенных PR
Статус: done
Фаза: P1
Тип: task
Владелец файлов: docs/tasks/WIRING-1460.md, docs/tasks/WIRING-1582.md, docs/tasks/WIRING-1608.md, docs/tasks/WIRING-1612.md
Зависимости: FTE-01, FTE-02, FTE-03
Ссылка: AGENTS.md:117,127, docs/templates/orchestrator-task.md:1195, docs/templates/wiring-card.md:1-17, docs/research/research-feature-gaps.md:14,49
Агент: нет
Время: нет
baseline: git ls-tree origin/main -- docs/tasks/ не содержит WIRING-1460, WIRING-1582, WIRING-1608 и WIRING-1612. В репозитории есть только WIRING-485..493, WIRING-887, WIRING-915, WIRING-919, WIRING-926, WIRING-931 и WIRING-1106.
Цель: Отложенная проводка четырёх PR видна оркестратору через beacon deferred --format json и обрабатывается первой на следующей волне.
Контекст: AGENTS.md:117 требует вызывать beacon_defer_wiring сразу, как только часть фичи осталась неподключённой. docs/templates/orchestrator-task.md:1195 ставит записи со статусом todo в начало очереди волны. PR 1460, 1582, 1608 и 1612 оставили проводку вне своих OwnerFiles и не завели записи. Записи без карточки оркестратор не увидит.
Шаги: Для каждого PR завести карточку по docs/templates/wiring-card.md. Вызвать beacon_defer_wiring с watchID исходного PR. Сверить, что запись появилась в beacon deferred --format json. Не менять код номеров PR.
Проверка: beacon deferred --format json содержит четыре записи с нужными номерами PR и статусом todo. Каждая ссылается на существующий файл WIRING-<pr>.md с заполненным OwnerFiles и разделом Причина.
Приёмка: Четыре записи видимы оркестратору, карточки материализованы в docs/tasks/, индекс обновлён CI.

Причина: Уже влито в origin/main: коммит 79e70595 (PR #1900) добавил docs/tasks/WIRING-1460.md, WIRING-1582.md, WIRING-1608.md, WIRING-1612.md. git merge-base --is-ancestor 79e70595 origin/main дал exit 0. Все четыре карточки материализованы с заполненными OwnerFiles и разделом Причина. Дубль кода не создаём.
Закрыл: fte04b
Время закрытия: 2026-11-22T16:30:37Z