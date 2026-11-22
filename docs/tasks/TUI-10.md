# TUI-10. Клавиши spawn и kill на human-decide
Статус: dropped
Фаза: P1
Тип: task
Владелец файлов: internal/domain/types.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не обещать в UI commands, которых нет в контракте human-decide.
Контекст: Источник: docs/research/audit-03-tui.md, раздел про горячие клавиши. internal/infra/httpapi/server.go:989 разрешает approve, reject, context, rework, pause, resume, unblock и cancel. Verb spawn и verb kill в списке отсутствуют.
Шаги: Дизайн брошен. Сначала домен должен определить такие действия и маршрут сервера, затем можно вернуть клавиши.
Проверка: Проверка не требуется.
Приёмка: Подсказка экрана не обещает spawn и kill.
