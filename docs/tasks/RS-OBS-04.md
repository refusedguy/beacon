# RS-OBS-04. Политика NO_CONTENT для телеметрии
Статус: done
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

Решение: в observe.go введено перечисление `ContentCaptureMode` с режимами `ContentCaptureNoContent` (по умолчанию) и `ContentCaptureEnabled` (явный opt-in). Парсер `ParseContentCaptureMode` разбирает значение OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT и при неизвестном значении возвращает NoContent. В production ключ читает setupObserve в cmd/beacon/observe.go: он читает переменную один раз до установки логгера, прогоняет значение через парсер и ставит режим через SetContentCapture. По умолчанию действует NoContent. Граница записи SanitizeContent удаляет четыре категории содержимого: текст wake, аргументы инструментов, системные инструкции и diff PR. Вместо текста сохраняется только FieldContentRef, ссылка на внешнее хранилище. Без переданной ссылки пишется маркер "withheld". Санитизация применена в Recorder.Log и Event. В observe_test.go добавлены тесты на каждый режим, на парсер, на сброс к NoContent, на попытку записать содержимое без opt-in через Recorder и Event, а также на сохранение текста при opt-in. Тестовые строки синтетические, реальный diff PR не пишется. gofmt применён, go test локально не запускался, проверка идёт в CI.

## Подтверждение
- Premise перепроверен 2026-11-21 по свежему origin/main (527084c). Реализация влита PR #866, коммит f0a0db3, merged 2026-11-21. В internal/infra/observe/observe.go на main есть тип ContentCaptureMode с режимами ContentCaptureNoContent по умолчанию и ContentCaptureEnabled как явный opt-in, парсер ParseContentCaptureMode для OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT и граница записи SanitizeContent. Функция удаляет текст wake, аргументы инструмента, системные инструкции и diff PR, сохраняя только FieldContentRef или маркер "withheld". Тесты в observe_test.go на main закрывают каждый режим, парсер, сброс в NoContent и попытку записать содержимое без opt-in через Recorder и Event. Тестовые строки синтетические. Открытых PR по карточке нет, дубля нет. Статус изменён с todo на done со ссылкой на PR #866. OwnerFiles не менял, код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, правка только текстовая. Уточнение CE-01 (2026-11-22): на момент подтверждения парсер и константа были в observe.go, но production-читателя не было. setupObserve теперь читает ключ до установки логгера и ставит режим через SetContentCapture, поэтому обещание env-управления в комментарии и в этой карточке выполняется.
