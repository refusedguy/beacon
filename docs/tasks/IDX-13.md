# IDX-13. Метрика dual-read и условие выхода
Блокер: точный блокер с именем файла `internal/infra/taskstore/store.go`. Функция `LoadCards` (строка 286) по-прежнему читает карточки через `os.ReadFile` напрямую и не проходит через `FileAdapter`. PR #910 (коммит 54d2123, MERGED) влил счётчик `file_adapter_reads_total` и `FileAdapter.ReadFile` в `internal/infra/taskstore/load.go`, но покрывает только путь адаптера. Приёмка «метрика позволяет доказать ноль обращений за семь дней» не закрыта: нужен отдельный PR с OwnerFiles на `internal/infra/taskstore/store.go`, переводящий `LoadCards` на адаптер. Статус todo сохранён.
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/taskstore/load.go, internal/infra/observe/metrics.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Счётчик обращений к файловому адаптеру и правило нуля за семь дней.
Контекст: Premise: владелец согласился, что dual-read заканчивается по метрике, а не по дате. baseline: до изменения в store.go было 4 вызова os.ReadFile, счётчика file_adapter_reads_total в metrics.go не было.
Шаги: Инкремент при каждом чтении файла, экспорт в beacon metrics, тест на рост и сброс.
Проверка: gofmt, go test в CI.
Приёмка: Метрика видна и позволяет доказать ноль обращений за семь дней.

## Ход

- 2026-11-21: добавлен internal/infra/taskstore/load.go с FileAdapter.ReadFile. Каждый доступ к файлу через адаптер вызывает observe.FileAdapterRead. LoadCards в store.go не тронут.
- Счётчик file_adapter_reads_total зарегистрирован в extendedCounterNames. Он виден в JSON, Prometheus и снимке метрик. Реестр counterLabels остался из 13 имён, поэтому fixed-name тест не ломается.
- Тест TestFileAdapterReadsCounterGrowsAndResets проверяет рост до 3, экспорт в Prometheus и обнуление через Metrics.Reset.
- Ограничение: LoadCards всё ещё читает файлы через os.ReadFile напрямую и не попадает в счётчик. Чтобы доказать ноль по всем путям, его нужно перевести на FileAdapter отдельным изменением.
- gofmt выполнен. go test и go build по инструкции не запускались, проверка идёт в CI.

## Подтверждение
- Premise проверен 2026-11-21 по свежему origin/main (4943794). `git log origin/main -- internal/infra/taskstore/load.go internal/infra/observe/metrics.go` даёт 54d2123 (#910) как последнюю правку; `gh pr list --state all --search 'dual-read OR LoadCards OR FileAdapter'` даёт #910 (MERGED), #912 и #915, ни один из которых не переводит `LoadCards` на адаптер. `git show origin/main:internal/infra/taskstore/store.go` содержит `os.ReadFile` в строках 286, 433, 486 и 682; `LoadCards` не вызывает `FileAdapter`. Открытых PR с миграцией `LoadCards` нет. Часть работы влита, но карточку закрыть нельзя: остаётся точный блокер в `internal/infra/taskstore/store.go`. Статус todo сохранён, код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.
- Перепроверка 2026-11-21 по свежему origin/main (4943794). Все коммиты c716b61..4943794 относятся к docs(tasks), код не менялся. `git log c716b61..origin/main -- internal/infra/taskstore/store.go internal/infra/taskstore/load.go internal/infra/observe/metrics.go` пуст. Повторный `gh pr list --state all --search 'dual-read OR LoadCards OR FileAdapter OR file_adapter_reads'` даёт только #910, #912 и #915. Миграции `LoadCards` нет. Блокер на месте без сдвига строк: `git show origin/main:internal/infra/taskstore/store.go` даёт `os.ReadFile` в строках 286, 433, 486, 682 и ни одного `FileAdapter`. Статус todo сохранён.
- Перепроверка 2026-11-21 по свежему origin/main (4a20420). baseline: `git rev-parse --short origin/main` = 4a20420. Реализация не влита. Все коммиты 4943794..4a20420 относятся к docs(tasks), код не менялся: `git log 4943794..origin/main -- internal/infra/taskstore/store.go internal/infra/taskstore/load.go internal/infra/observe/metrics.go` пуст. `gh pr list --state all --search 'dual-read OR LoadCards OR FileAdapter OR file_adapter_reads'` даёт #910, #912, #915 и docs-перепроверку #1004, PR с миграцией `LoadCards` нет. Блокер на месте без сдвига строк: `git grep -n -E 'func LoadCards|os\.ReadFile|FileAdapter' origin/main -- internal/infra/taskstore/store.go` даёт `LoadCards` на строке 275 и `os.ReadFile` на строках 286, 433, 486, 682, вхождения `FileAdapter` нет. Повторный `git fetch origin main` поднял main до 7c7e5c2, изменений в перечисленных файлах нет, вывод сохраняется. Статус todo сохранён, код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.
