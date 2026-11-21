# IDX-13. Метрика dual-read и условие выхода
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
