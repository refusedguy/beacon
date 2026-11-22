# FTE-10. Сделать ребейзы в отчёте оркестратора адресуемыми
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, cmd/beacon/orchestrate.go
Зависимости:
Ссылка: AGENTS.md:86, README.md:86, docs/research/research-feature-gaps.md:19,65
Агент: нет
Время: нет
baseline: internal/app/orchestrator/orchestrator.go:318 объявляет NeedsRebase как []int. cmd/beacon/orchestrate.go:299-300 печатает REBASE pr=<n>. watchID и revision в отчёте отсутствуют.
Цель: Потребитель beacon orchestrate --format json может привязать каждый ребейз к Watch и ревизии и получить nextAction из beacon_get_result.
Контекст: AGENTS.md:86 требует выполнять команду из nextAction, а не угадывать её по номеру PR. README.md:86 задаёт точную команду rebase. Сейчас в отчёте есть только номер PR, поэтому вызвать beacon_get_result для конкретного ребейза нельзя.
Шаги: Заменить []int на тип с полями watchID, revision и pr. Заполнять его из обработанных Watch. Обновить табличный рендер и JSON-тест. Сохранить совместимость имени поля needsRebase.
Проверка: В JSON каждый элемент needsRebase содержит watchID, revision и pr. По watchID вызывается beacon_get_result и возвращает nextAction: rebase. Табличный вывод показывает watchID рядом с номером PR.
Приёмка: Отчёт адресуем, тест JSONShape обновлён, CI зелёный.
