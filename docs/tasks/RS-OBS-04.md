# RS-OBS-04. Политика NO_CONTENT для телеметрии
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/observe/observe.go, internal/infra/observe/observe_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Зафиксировать, что текст wake, аргументы инструментов, системные инструкции и diff PR попадают в телеметрию только по явному opt-in.
Контекст: Premise: проверено 2026-11-21, единой политики захвата содержимого нет. baseline: `grep -rn 'NO_CONTENT\|CAPTURE_CONTENT' internal/infra/observe` пусто. Источник: OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT с режимом NO_CONTENT по умолчанию.
Шаги: Ввести перечисление режимов и проверку на границе записи. По умолчанию сохранять только ссылку на внешнее хранилище, а не текст. Тест на каждый режим и на попытку записать содержимое без opt-in.
Проверка: gofmt чистый. Тесты в CI. Ни один тест не пишет реальный diff PR.
Приёмка: Без явного разрешения в логах и спанах нет текста сообщений.

Premise: проверено повторно 2026-11-21. `grep -rn 'NO_CONTENT\|CAPTURE_CONTENT' internal/infra/observe` пусто. ADR 0267 объявляет NO_CONTENT, но перечисления и проверки на границе записи в observe нет. Частичная фильтрация атрибутов спанов лежит в `internal/infra/otel/local.go` (RS-OBS-02): там жёстко отбрасываются gen_ai-ключи с текстом. Это тот же default, но не перечисление с opt-in, и файл не входит в OwnerFiles карточки. Блокера по OwnerFiles нет: фильтр спанов уже закрывает приёмку для спанов, а данная карточка закрывает границу логов в observe.go.

baseline: `grep -rn 'NO_CONTENT\|CAPTURE_CONTENT' internal/infra/observe` = 0 совпадений до правки. Источник: OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT, ADR 0267.

Решение: в observe.go введено перечисление `ContentCaptureMode` с режимами `ContentCaptureNoContent` (по умолчанию) и `ContentCaptureEnabled` (явный opt-in). Парсер `ParseContentCaptureMode` разбирает значение OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT и при неизвестном значении возвращает NoContent. Глобальный режим ставится через SetContentCapture, по умолчанию действует NoContent. Граница записи SanitizeContent удаляет четыре категории содержимого: текст wake, аргументы инструментов, системные инструкции и diff PR. Вместо текста сохраняется только FieldContentRef, ссылка на внешнее хранилище. Без переданной ссылки пишется маркер "withheld". Санитизация применена в Recorder.Log и Event. В observe_test.go добавлены тесты на каждый режим, на парсер, на сброс к NoContent, на попытку записать содержимое без opt-in через Recorder и Event, а также на сохранение текста при opt-in. Тестовые строки синтетические, реальный diff PR не пишется. gofmt применён, go test локально не запускался, проверка идёт в CI.
