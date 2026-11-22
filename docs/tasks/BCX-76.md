# BCX-76. Свести три имени корня проекта к одному
Статус: done
Фаза: P1
Тип: task
Владелец файлов: scripts/verify-beacon.sh, cmd/beacon/task.go, cmd/beacon/doctor.go, cmd/beacon/mcp.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать BEACON_REPO, BEACON_ROOT и BEACON_PROJECT_ROOT с разными default и потребителями.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 6 и раздел «Разные default» пункт 4. BEACON_REPO читают scripts/verify-beacon.sh:8 и verify-daemon.sh:8; BEACON_ROOT в task.go:700; BEACON_PROJECT_ROOT в doctor.go:249 и mcp.go:1143. Единого приоритета нет. BEACON_REPO описан в docs/runbooks/verification.md:16, но в Go-коде отсутствует. Базовый замер и приоритет из отчёта: P1.
Шаги: Выбрать каноническое имя и приоритет. Перевести скрипты и плагин на него, оставить остальные как deprecated alias с предупреждением. Обновить runbook. Добавить тест на согласованный root во всех потребителях.
Проверка: CI зелёный. Все consumer получают одинаковый root. Устаревшие имена либо удалены, либо выдают предупреждение.
Приёмка: Конфликт имён корня устранён, и документ описывает один приоритет.

Итог: Already landed in PR #1348 (commit f7049b4850c04edce68e22c29d1b982253c4ad4c). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1348 merged 2026-11-22T09:35:00+05:00; commit f7049b4850c04edce68e22c29d1b982253c4ad4c; git merge-base --is-ancestor f7049b4850c04edce68e22c29d1b982253c4ad4c origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1348 f7049b48
Время закрытия: 2026-11-22T11:24:28Z
