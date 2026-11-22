# ADR 247: Документация как отдельный гейт

Статус: open

## Контекст

Harbor developer workflow: docs-artifacts. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: ci.yml игнорирует **.md и docs/ намеренно, docs-only PR идёт в отдельный docs.yml с check-md-links, md-lint и self-test на плохом вводе. Beacon переносит только идею отдельного гейта. Его docs.yml запускает docsgate с self-test и проверкой статуса, структуры и ревизий карточек. Шагов check-md-links и md-lint в docs.yml нет. Проверка ссылок и якорей лежит в gate.CheckLinks (HD-086) и не подключена к docsgate. Отдельного md-lint в репозитории нет.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
