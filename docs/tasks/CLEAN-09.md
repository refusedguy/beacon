# CLEAN-09. Закрыть RS-OBS-02 и удалить локальный OTEL sink
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/otel/, docs/tasks/RS-OBS-02.md
Зависимости: RS-OBS-02
Ссылка: PR #1166 (https://github.com/refusedguy/beacon/pull/1166)
Агент: нет
Время: нет
Цель: Подтвердить, что observability переехала в internal/infra/observe, закрыть RS-OBS-02 и удалить дублирующий локальный sink.
Контекст: Источник: docs/research/deadcode-audit.md, строка про internal/infra/otel. 410 не-тестовых строк, 0 импортёров. Реализация в internal/infra/observe/otel.go имеет 31 импортёра. Риск: средний. Нужен owner-decision по судьбе карточки RS-OBS-02, которая ещё todo.
Шаги: Сверить функции старого и нового sink. Закрыть RS-OBS-02 через batch-apply с указанием PR. Удалить каталог internal/infra/otel после закрытия карточки.
Проверка: CI зелёный, RS-OBS-02 закрыта, импортов internal/infra/otel нет.
Приёмка: Старый sink удалён только после закрытия RS-OBS-02. До решения владельца карточка остаётся todo.

## Подтверждение
- 2026-11-21, проверка по свежему origin/main (fcb2e8d). `rg 'infra/otel' internal` и поиск по `*.go` не нашли ни импортов, ни ссылок на internal/infra/otel.
- Трассировка живёт в internal/infra/observe. Пакет internal/infra/observe/otel.go определяет Tracer, SpanSink, Span и OTLPExporter. 29 файлов в internal импортируют observe. Файл tools/beacon-gates/gate/config/layers.json объявляет internal/infra/observe и не упоминает otel. В reachability.json ссылок на otel нет.
- Старый пакет internal/infra/otel содержал локальный NDJSON sink с ротацией, функции ReadAll и ReadStateDir и пять GenAI-атрибутов. Чтение файла и ротация в observe не перенесены. У пакета не было импортёров, поэтому удаление не меняет поведение во время работы.
- RS-OBS-02 уже закрыта в main. Реализация вошла в PR #854, коммит 43d2788. В карточке заполнена строка Ссылка. Отдельное решение владельца не потребовалось.
- Удалены internal/infra/otel/local.go и internal/infra/otel/local_test.go. gofmt не применялся, .go-файлов после удаления нет. go test, go build и гейты не запускались локально, проверка идёт в CI.
- Ссылка на этот PR: PR #1166 (https://github.com/refusedguy/beacon/pull/1166)
