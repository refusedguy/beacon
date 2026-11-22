# MEM-05. Удалять сессии закрытых Watch в beacon gc
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/gc.go, cmd/beacon/gc_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Замкнуть жизненный цикл сессии после закрытия Watch.
Контекст: Источник: docs/research/audit-02-memory.md, строка таблицы про сессии. cmd/beacon/spawn.go:194 создаёт сессию, :95 записывает её id, но удаления нет. cmd/beacon/gc.go:70 уже регистрирует сборщик serve, :173 содержит runGCWithServe. Для кандидатов подходит store.RecentClosedWatches в internal/infra/store/archive.go:278. Живые Watch защищены через LiveWatches, тот же приём используется в cmd/beacon/mcp.go:1883. Baseline: ноль вызовов DELETE /api/session в gc.go. Удаление сессии активного подписчика ломает wake, поэтому защита обязательна.
Шаги: Добавить в GC проход по RecentClosedWatches с окном удержания. Собрать sessionID из подписчиков закрытого Watch, исключить id из LiveWatches и id текущей сессии. Вызвать DeleteSession из MEM-01 только при --apply, в dry-run печатать план. Записать результат в отчёт GC и в метрику. Покрыть gc_test.go тестами на активного подписчика, на закрытого и на dry-run.
Проверка: CI зелёный. Тест с активным Watch не вызывает удаление. Тест с закрытым Watch и --apply вызывает DeleteSession один раз.
Приёмка: Сессии закрытых Watch удаляются. Живые подписчики и текущая сессия защищены.

Итог: Already landed in PR #1444 (commit b1c647615f0d17bafa30899ad4b77594443aef48). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1444 merged 2026-11-22T09:57:50+05:00; commit b1c647615f0d17bafa30899ad4b77594443aef48; git merge-base --is-ancestor b1c647615f0d17bafa30899ad4b77594443aef48 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1444 b1c64761
Время закрытия: 2026-11-22T11:25:31Z
