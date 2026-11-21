# BCX-57. Написать сценарий human-decide с продолжением той же Watch
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/human_test.go, docs/evals/w21-human-question-revision.md, docs/evals/e05-wait-pending-question.md
Зависимости: 
Ссылка: ADR 0061
Агент: нет
Время: нет
Цель: Закрыть пробел в сценарии needs_input_continue_of_same_watch.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 6. Сценарий needs_input_continue_of_same_watch из ADR 0061 пункт 4 не написан. Есть только cmd/beacon/human_test.go и спецификации W21 и E05. Базовый замер и приоритет из отчёта: P1.
Шаги: Собрать сценарий: Watch получает needs_input, агент отвечает human approve, ta же Watch продолжается с тем же watchID. Проверить, что не создаётся новая Watch и обязательство не теряется. Добавить негатив с изменившимся вопросом.
Проверка: CI зелёный. Живой сценарий проходит и видит тот же watchID до и после ответа. Негатив с изменённым вопросом отклоняется.
Приёмка: Продолжение после ответа человека работает на одной Watch и подтверждено е2е.
