# BCX-22. Сделать --help рабочим для каждой команды
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/catalog.go, cmd/beacon/main.go, cmd/beacon/task.go, cmd/beacon/spawn.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Зарегистрировать справку по команде и вернуть код 0 в stdout для --help.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 2. parseFlags в catalog.go:38-51 ставит fs.Usage = func(){} и при любой ошибке печатает общий usage в stderr с кодом 2. task и spawn используют parseTaskArgs (task.go:113-147), где --help это неизвестный флаг с кодом 1. Базовый замер и приоритет из отчёта: P0.
Шаги: Зарегистрировать fs.Usage для каждого FlagSet. Обработать flag.ErrHelp как код 0 и писать справку в stdout. Ввести единый формат. Обновить README, который рекомендует beacon what как справку.
Проверка: CI зелёный. Для каждой команды <cmd> --help даёт код 0 и справку в stdout. Тест перебирает все команды.
Приёмка: Агент получает справку по конкретной команде без общего usage и кода 2.

Итог: Already landed in PR #1284 (commit 006e00da7651c8b4dd146b04b734b395d78fbb67). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1284 merged 2026-11-21T22:51:09+05:00; commit 006e00da7651c8b4dd146b04b734b395d78fbb67; git merge-base --is-ancestor 006e00da7651c8b4dd146b04b734b395d78fbb67 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1284 006e00da
Время закрытия: 2026-11-22T11:24:28Z
