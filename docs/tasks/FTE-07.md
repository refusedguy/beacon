# FTE-07. Принять context и force_retry_merge в beacon human
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/human.go, cmd/beacon/human_test.go, cmd/beacon/catalog.go
Зависимости:
Ссылка: README.md:333, docs/evals/tui-live-single-screen.md:26,32,36, docs/evals/w21-human-question-revision.md:4021, docs/research/research-feature-gaps.md:17,75
Агент: нет
Время: нет
baseline: humanOutcomes в cmd/beacon/human.go:47 перечисляет семь verbs. internal/infra/httpapi/server.go:1354-1358 принимает девять, включая context и force_retry_merge. internal/tui/app.go:112,118 предлагает девять клавиш. Расхождение равно двум verbs.
Цель: CLI принимает все девять команд human gate, которые принимает сервер и живой экран.
Контекст: README.md:333 обещает ответ context через human gate. docs/evals/tui-live-single-screen.md:26 и :32 предписывают клавиши c и f с соответствующими verbs в POST. cmd/beacon/human.go:35-50 отвергает context и force_retry_merge с кодом 2. Оператор видит клавишу на экране, но не может выполнить её из CLI.
Шаги: Добавить humanContext и humanForceRetryMerge в humanOutcome и humanOutcomes. Обновить Usage в cmd/beacon/human.go:24. Покрыть тестом разбора всех девяти verbs и отрицательным тестом неизвестного глагола. Проверить, что note по-прежнему сохраняется.
Проверка: beacon human context <watchID> --note "..." возвращает код 0. beacon human force_retry_merge <watchID> возвращает код 0. Неизвестный verb возвращает код 2 без записи. Список humanOutcomes совпадает с validCommands сервера.
Приёмка: Набор verbs CLI совпадает с сервером, CI зелёный.
