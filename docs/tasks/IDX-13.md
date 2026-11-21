# IDX-13. Метрика dual-read и условие выхода
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/taskstore/store.go, internal/infra/taskstore/store_test.go, docs/tasks/IDX-13.md
Зависимости: 
Ссылка: PR #1105
Агент: нет
Время: нет
Цель: Счётчик обращений к файловому адаптеру и правило нуля за семь дней.
Контекст: Premise: владелец согласился, что dual-read заканчивается по метрике, а не по дате. baseline: до изменения в store.go было 4 вызова os.ReadFile, из них 2 чтения карточек в LoadCards и ReadTaskEvents и 2 служебных чтения lock и counter.
Шаги: Инкремент при каждом чтении файла, экспорт в beacon metrics, тест на рост и сброс.
Проверка: gofmt, go test в CI.
Приёмка: Метрика видна и позволяет доказать ноль обращений за семь дней.

## Ход

- 2026-11-21: PR #910 (коммит 54d2123) влил FileAdapter.ReadFile в load.go и счётчик file_adapter_reads_total в metrics.go. LoadCards оставался на os.ReadFile.
- 2026-11-21: LoadCards и ReadTaskEvents переведены на fileAdapter.ReadFile. Служебные файлы lock и counter остались на os.ReadFile: они не являются markdown-источником, и их учёт не дал бы нуля в метрике.
- 2026-11-21: добавлен TestLoadCardsAndReadTaskEventsUseFileAdapter. Он проверяет рост счётчика до 2 после загрузки двух карточек и до 3 после ReadTaskEvents.
- gofmt выполнен. go test и go build по инструкции не запускались, проверка идёт в CI.

## Подтверждение

- 2026-11-21: реализация в PR #1105. LoadCards и ReadTaskEvents читают карточки через FileAdapter.ReadFile, счётчик file_adapter_reads_total растёт на каждое чтение карточки. Блокер снят, повторные перепроверки удалены. Статус done.
