# Q-C2. Завершение открытых карточек RS
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: docs/ROADMAP_STATUS.md, docs/tasks/RS-MCP-06.md, docs/tasks/RS-CMP-01.md, docs/tasks/RS-OBS-01.md, docs/tasks/RS-OBS-03.md, docs/tasks/RS-WK-03.md, docs/tasks/RS-WK-04.md, docs/tasks/RS-EVAL-01.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Все карточки RS либо закрыты ссылкой на влитый PR, либо несут точный блокер с именем файла.
Контекст: Premise: после перескопа и трёх волн большинство RS закрыто кодом, часть осталась с блокерами. Требуется финальный аудит statuses и card_closures.
Шаги: Сверить статусы с PR, закрыть сделанные через closures, оставить живые блокеры в todo, обновить роадмап.
Проверка: gofmt не применяется, docs gate в CI.
Приёмка: Нет карточек с устаревшим todo там, где код уже в main.
