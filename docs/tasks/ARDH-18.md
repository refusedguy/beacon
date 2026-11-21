# ARDH-18. Сохранить причину ошибки через %w в 35 местах
Статус: done
baseline: Замер 2026-11-21 по origin/main b9937c0: `rg -U` по восьми OwnerFiles нашёл 31 вызов fmt.Errorf с %v (agents 16, client 10, attach 4, bus 1) и 4 места в telegram.go с sanitize без %w. acp.go, daemon.go и observe.go уже используют %w. Источник предписания: docs/research/error-handling-audit.md:28-40, находка 2.
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/agents.go, internal/infra/opencode/client.go, cmd/beacon/attach.go, internal/infra/wake/acp.go, internal/app/orchestrator/daemon.go, internal/app/event/bus.go, cmd/beacon/observe.go, internal/infra/wake/telegram.go
Зависимости: 
Ссылка: ADR 0009
Агент: нет
Время: нет
Цель: Заменить форматирование ошибки %v на %w там, где текст не очищается, и добавить sentinel ErrTelegramTransport для четырёх мест с sanitize.
Контекст: Источник: docs/research/error-handling-audit.md, находка 2 и рекомендация 2. Всего 39 мест: agents.go 16, opencode/client.go 10, telegram.go 4, attach.go 4, acp.go 2 и по одному в daemon.go, event/bus.go, observe.go. Риск: низкий. В telegram.go потеря цепочки это плата за удаление токена, поэтому очистку текста сохранить.
Шаги: Заменить 35 вызовов без очистки, оставив текст ошибки прежним. В четырёх местах telegram.go обернуть sentinel через %w и держать очищенный текст в сообщении. Обновить тесты errors.Is и errors.As.
Проверка: CI зелёный. Тесты на цепочку ошибок проходят для затронутых пакетов.
Приёмка: Цепочка cause сохраняется в 35 местах, токены в логах telegram не появляются.

## Подтверждение
PR #1190 (fix/ardh18-wrap-errors) заменил 30 вызовов `%v` на `%w` в agents.go, attach.go и opencode/client.go без изменения текста ошибки. Четыре ошибки telegram.go получили sentinel `ErrTelegramTransport` через `%w`, а очистка токена через `sanitize` осталась на месте. В event/bus.go error-valued panic оборачивается через `%w`, не-error panic сохраняет `%v`. Добавлены проверки `errors.Is` для sentinel и для panic cause. Индекс docs/ROADMAP_STATUS.md обновлён по результату cardindex: done 659, dropped 326, todo 7, приоритеты P1 1 и P2 6. Изменение счётчиков вызвано тем, что main ушёл вперёд и принёс закрытые карты ARDH-17 и ARDH-19; их OwnerFiles перекрылись с ещё одной todo-картой P2.
