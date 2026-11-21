# Q-D2. beacon init и пилот на соседнем репозитории
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/init.go, cmd/beacon/commands.go, docs/runbooks/beacon-init.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Одна команда ставит хуки, конфиг и проверяет MCP в любом проекте.
Контекст: Premise: сейчас установка Beacon в новый репозиторий делается вручную. Требуется beacon init и пилот на Harbor-Harness с длинными проверками и SKIPPED job-ами.
Шаги: Реализовать команду, шаблоны .beacon/config.yaml и flaky.json, smoke проверку MCP, документировать пилот.
Проверка: gofmt, go test в CI.
Приёмка: beacon init в чистом репозитории создаёт все файлы и проходит проверку подключения.
