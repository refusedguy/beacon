# BCX-82. Ограничить tools.beacon и безопасный shell на границе инструмента
Статус: done
Фаза: P1
Тип: task
Владелец файлов: .opencode/plugins/beacon-project/index.js, .opencode/agents/issue-scout.md, AGENTS.md, docs/research/process-leak-mechanism.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Закрыть дыру, через которую research-агент вызывает execute и tools.beacon.*, и запретить рекурсивный поиск хуком.
Контекст: Источник: docs/research/process-leak-mechanism.md, находки 1, 4, 5 и меры (a), (b); process-leak-rootcause.md RC-1, RC-4, RC-5. Каждый вызов tools.beacon.* поднимает beacon mcp, который спавнит opencode serve; 20 вызовов дают около 20 процессов. issue-scout.md:1-14 запрещает edit, shell, write, но не execute и beacon_*. Плагин регистрирует MCP для каждой сессии с корнем проекта. .opencode/settings.json:3-16 содержит PreToolUse, но нет доказательства, что OpenCode 2.3.2 его читает. Базовый замер и приоритет из отчёта: P1.
Шаги: Создать профиль beacon-researcher с deny для execute, beacon_*, edit, shell, subagent и allow для webfetch. Запретить и execute, и beacon_*, иначе инструменты станут прямыми. Настроить скиллы research и night-shift на запуск только этим профилем. Написать плагин OpenCode с ctx.tool.hook(execute.before), который отклоняет grep -r, grep -R, find /, go test, go build, beacon gates. Неделю писать предупреждение в лог перед включением deny. Не трогать opencode.json в этом PR.
Проверка: CI зелёный. Тестовая сессия показывает, что research-агент не видит execute и beacon_*. Хук отклоняет запрещённые команды и называет замену rg с явным каталогом.
Приёмка: Research-роль не может запустить цепочку spawn через MCP. Рекурсивный поиск и локальные гейты останавливаются на границе инструмента, а не текстом в AGENTS.md.

Итог: Already landed in PR #1353 (commit c35c727900f30da652fee0c6bfdb450e95e72a54). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1353 merged 2026-11-22T09:39:30+05:00; commit c35c727900f30da652fee0c6bfdb450e95e72a54; git merge-base --is-ancestor c35c727900f30da652fee0c6bfdb450e95e72a54 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1353 c35c7279
Время закрытия: 2026-11-22T11:24:28Z
