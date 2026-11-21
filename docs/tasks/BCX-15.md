# BCX-15. Объединить шесть определений кодов ошибок и две таблицы статусов
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/server/http/error.go, internal/infra/httpapi/server.go, internal/infra/mcp/schema.go
Зависимости: 
Ссылка: ADR 0009
Агент: нет
Время: нет
Цель: Убрать дублирование кодов и guaranteed нулевой статус у новых ошибок.
Контекст: Источник: docs/research/error-codes-audit.md, P1 «Коды ошибок распределены по шести типам». domain/types.go:498 объявляет ErrorCode, но новые коды добавляются в github/errors.go:370, prcreate/service.go:15, tool_list_running.go:175. server/http/error.go:14 объявляет свой Code в нижнем регистре. statusForCode в types.go:525 и statusByCode в schema.go:372 дублируют таблицу; REJECT_DUPLICATE, CI_*, RECLAIM_* в неё не включены и получают статус 0. Базовый замер и приоритет из отчёта: P1.
Шаги: Выбрать канонический реестр кодов. Перевести infra-определения на него или явно зафиксировать исключения. Объединить таблицы статусов. Добавить проверку, что StatusForCode не возвращает 0 для известного кода.
Проверка: CI зелёный. Тест перебирает все известные коды и требует ненулевой HTTP-статус. Дубли типов удалены или помечены.
Приёмка: Новый код, созданный через NewAppError, получает корректный статус. Один смысл кодируется одним literals.
