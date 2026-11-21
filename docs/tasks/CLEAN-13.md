# CLEAN-13. Удалить internal/app/tasks после закрытия WIRING-487 и WIRING-489
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/tasks/, docs/tasks/WIRING-487.md, docs/tasks/WIRING-489.md
Зависимости: WIRING-487, WIRING-489
Ссылка: 
Агент: exe79
Время: 2026-11-21T17:44:48Z
baseline: `git rev-parse --short origin/main` = b9937c01; обе wiring-карточки имеют `Статус: done`; `rg -n 'refusedguy/beacon/internal/app/tasks' internal cmd tools tests` даёт cmd/beacon/daemon.go:18 и cmd/beacon/reminders_test.go:9; `rg -n 'Poll\(' internal/app/tasks/card_monitor.go` даёт строку 155; `git merge-base --is-ancestor 94f6d29c origin/main` и то же для 370041d8 возвращают true.
Цель: Разобраться с двумя wiring-карточками и решить, удалять ли card monitor и night shift.
Контекст: Источник: docs/research/deadcode-audit.md, строка про internal/app/tasks. 673 не-тестовых строки, 0 импортёров. Обе wiring-карточки в todo и содержат блокировщик по OwnerFiles и layers.json. Риск: высокий. Нужен owner-decision: удаление закроет запланированную проводку.
Шаги: Сверить OwnerFiles двух карточек с текущими файлами. Получить решение владельца по проводке. При отказе закрыть карточки и удалить каталог.
Проверка: WIRING-487 и WIRING-489 закрыты или переведены в работу. CI зелёный.
Приёмка: Пакет удалён только после решения владельца. Карточка остаётся todo.

## Причина

Premise из поля «Контекст» устарел. Когда писали карточку, обе wiring-карточки были в todo, а у пакета было ноль импортёров. По свежему origin/main b9937c01 обе карточки имеют статус done. WIRING-487 закрыта PR #1153, WIRING-489 закрыта PR #1156. Оба коммита достижимы из origin/main.

Проводка выполнена. В cmd/beacon/daemon.go есть daemonCardPort, newDaemonCardMonitor, daemonPollCards и runDaemonCardMonitor. Монитор создаётся один на процесс, а Poll вызывается в проходе --once и по расписанию. Пакет internal/app/tasks импортируется в cmd/beacon/daemon.go и cmd/beacon/reminders_test.go. Условие удаления в карточке требует одновременно закрытых карточек и отсутствия импортов. Второе условие не выполняется, поэтому каталог не удалён.

Отдельно по night shift. Файл internal/app/tasks/night_shift.go определяет экспортированный Worker, но не имеет импортёров. Wiring-карточки для него нет. Решения владельца по удалению нет, поэтому файл оставлен. Удаление требует отдельного решения владельца.

## Подтверждение

- Проверено 2026-11-21 по origin/main b9937c01. Обе wiring-карточки в статусе done, коммиты PR #1153 (94f6d29c) и PR #1156 (370041d8) достижимы из origin/main.
- Удаление не выполнялось. internal/app/tasks импортируется в cmd/beacon/daemon.go и cmd/beacon/reminders_test.go, метод Poll на строке 155 вызывается из демона.
- Карточка остаётся todo. Причина зафиксирована выше. Решение владельца по night shift не выдумывалось.
- PR, изменившие premise: [#1153](https://github.com/refusedguy/beacon/pull/1153) и [#1156](https://github.com/refusedguy/beacon/pull/1156).
