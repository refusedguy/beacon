# ATX-C07. Согласовать лимиты таблиц исключений гейтов
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: tools/beacon-gates/gate/config/baseline.json, tools/beacon-gates/gate/config/docs-baseline.json, tools/beacon-gates/gate/config/permanent.json, tools/beacon-gates/gate/exceptions.go
Зависимости: 
Ссылка: docs/research/ci-gates-gaps.md, находка C07
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Убрать расхождение между объявленными лимитами исключений и фактическим содержимым таблиц.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C07. Замер: `baseline.json` содержит `baseline_limit: 0` и пустой `baseline`. `docs-baseline.json` содержит `baseline_limit: 40` и пустой `baseline` после #1264. `permanent.json` содержит 19 строк и не имеет лимита. `tools/beacon-gates/gate/exceptions.go:121` проверяет лимит только для baseline, а permanent проверяется только на актуальность. У baseline обязательно поле `task`, у permanent поле `reason`.
Шаги: Привести `baseline_limit` в docs-baseline.json к нулю или к числу, которое соответствует политике и описано в документации. Добавить self-test на согласованность: лимит не меньше числа строк, правила не пересекаются между таблицами, у каждой строки есть обязательное поле. Описать политику permanent-исключений в `docs/gates/`. Не удалять существующие permanent-строки без доказательства их неактуальности.
Проверка: CI зелёный. Self-test ловит посаженную строку с лимитом меньше числа записей и посаженное пересечение правил. Прогон гейтов на дереве не меняет список находок.
Приёмка: Лимиты соответствуют содержимому таблиц. Пересечение правил между baseline и permanent невозможно.
