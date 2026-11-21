# BCX-71. Проверять адрес BEACON_GITHUB_API и связать его с основным клиентом
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/tool_get_result.go, cmd/beacon/mcp.go, internal/infra/github/graphql.go
Зависимости: 
Ссылка: 
Агент: exe49
Время: 2026-11-21T17:40:34Z
Цель: Не допустить отправки Bearer-токена на произвольный хост через конфиг.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 1. mcp/tool_get_result.go:573 читает BEACON_GITHUB_API, default https://api.github.com; :644 ставит Authorization: Bearer на каждый запрос. Схема, хост и путь не проверяются. Основной клиент использует захардкоженные адреса в mcp.go:1106 и github/graphql.go:27. Базовый замер и приоритет из отчёта: P0.
Шаги: Проверять схему http/https и whitelist хостов или Require explicit opt-in. Передать переменную основному GitHub-клиенту либо явно документировать, что она влияет только на failure excerpt. Добавить тест на неверный адрес и на Enterprise-хост.
Проверка: CI зелёный. Тест отвергает адрес с неверной схемой. Оба клиента используют один источник адреса или различие описано.
Приёмка: Токен не уходит на неожидаемый хост. Оператор получает тот же GitHub API во всех путях.

Итог: Уже влито в PR #1220 (карточка ATX-10), коммит c19fd7971d3c692b3508cdca67412a4643202e54
Доказательство: c19fd797 в origin/main, merge-base --is-ancestor=0; newEnvFailureExcerptSource проверяет BEACON_GITHUB_API через allowedExcerptBase/allowedExcerptURL, getJSON повторяет проверку хоста перед Authorization: Bearer
