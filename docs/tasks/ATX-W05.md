# ATX-W05. Убрать дубли и устаревшие результаты поллеров WatchOutcome
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/db.go, internal/infra/store/db_test.go
Зависимости: ATX-W03
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W05
Агент: нет
Время: нет
Цель: Делать один SQL-опрос на ключ WatchOutcome и не выдавать результат, опубликованный до подписки.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Поллеры WatchOutcome». PR #1228 научил `DB.Close` дожидаться горутин через `watchers.WaitGroup`, но не изменил сам поллер. `WatchOutcome` в `internal/infra/store/db.go:1041-1095` создаёт тикер на 50 мс на каждый вызов, а `readWatchOutcome` выбирает последнюю строку по ключу без сравнения ревизии. Новый наблюдатель получает старый результат сразу после подписки. Вызов с `context.Background()` без чтения канала держит горутину до закрытия store. Baseline: число опросов равно числу ожидающих, фильтра по ревизии нет.
Шаги: Добавить параметр минимальной ревизии и игнорировать строку с `revision <= want`. Сгруппировать ожидающих по ключу в один поллер с общим результатом и отдельными каналами. Требовать ctx с крайним сроком или добавить таймаут простоя для горутины. Обновить тесты `db_test.go` на случай публикации до подписки и двух ожидающих на один ключ.
Проверка: CI зелёный. Подписка с порогом 2 молчит после ревизии 1 и выдаёт ревизию 2. Два ожидающих на один ключ дают один запрос за такт. `DB.Close` по-прежнему дожидается всех поллеров.
Приёмка: Один поллер на ключ, результат не старше момента подписки, горутины не живут после закрытия store.

Итог: Cardindex resolves done: OwnerFiles overlap with T3.101 (already done). Supporting merged commit c293cf22bca7 (PR #1530) touches the card OwnerFiles.
Доказательство: Commit c293cf22bca7588b1ebd6bbcc3c3598395f6cb01 (PR #1530, fix(domain): renew parked Watch on repeat registration (LC-05) (#1530)) touches OwnerFiles internal/infra/store/db.go, internal/infra/store/db_test.go; git merge-base --is-ancestor c293cf22bca7588b1ebd6bbcc3c3598395f6cb01 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1530 c293cf22bca7
Время закрытия: 2026-11-22T11:27:01Z
