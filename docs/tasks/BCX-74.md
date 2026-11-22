# BCX-74. Проверять BEACON_ACP_COMMAND и BEACON_ACP_ARGS
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/spawn.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать возможность выбрать исполняемый файл через env без проверки и undocumented кавычки.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 4. spawn.go:249-269: BEACON_ACP_RUNNER default opencode, BEACON_ACP_COMMAND и BEACON_ACP_ARGS полностью заменяют command line. strings.Fields не понимает кавычки. Оболочки нет, но env определяет исполняемый файл. Базовый замер и приоритет из отчёта: P1.
Шаги: Проверять, что команда является разрешённым исполняемым файлом, или требовать абсолютный путь и явное согласие. Заменить strings.Fields на разбор с кавычками или документировать ограничение. Добавить тест на команду с пробелами в аргументе.
Проверка: CI зелёный. Тест на неразрешённую команду даёт ошибку. Аргумент с пробелом передаётся как одно значение или ограничение документировано.
Приёмка: Env не может тихо подменить runner. Способ передачи аргументов описан.

Итог: Already landed in PR #1333 (commit acce7fe9c968347339b1c1df5da93ad69ade6b2b). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1333 merged 2026-11-22T09:07:59+05:00; commit acce7fe9c968347339b1c1df5da93ad69ade6b2b; git merge-base --is-ancestor acce7fe9c968347339b1c1df5da93ad69ade6b2b origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1333 acce7fe9
Время закрытия: 2026-11-22T11:24:28Z
