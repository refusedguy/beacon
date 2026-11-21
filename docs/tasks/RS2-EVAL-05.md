# RS2-EVAL-05. Отдельная обязательная проверка для Broken
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/commands.go, tools/beacon-gates/gate/exceptions.go, docs/RULES-CHECK-CONTRACT.md, .github/workflows/ci.yml
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Передавать код 2 без преобразования в warn и сделать отдельную mandatory job, падающую на Broken.
Контекст: Premise: RS-EVAL-05 зафиксировала, что commands.go:49-73 ставит status warn при любом ненулевом коде, а BROKEN уже описан в контракте.
Шаги: Пробросить код 2, добавить джобу, зафиксировать контракт, тест на skipped и neutral.
Проверка: gofmt, go test и гейты в CI.
Приёмка: Отказ инструмента не выглядит зелёной проверкой.
