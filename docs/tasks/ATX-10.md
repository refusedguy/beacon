# ATX-10. Ограничить BEACON_GITHUB_API и кодировать параметры URL вырезок
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/tool_get_result.go, internal/infra/observe/log.go
Зависимости: 
Ссылка: 
Агент: exe90
Время: 2026-11-21T17:51:10Z
Цель: Не отправлять GitHub-токен на произвольный хост и не менять путь запроса через данные Watch.
Контекст: Источник: docs/research/security-audit.md, находка 2 и строка таблицы 150. internal/infra/mcp/tool_get_result.go:568-581 берёт базу из BEACON_GITHUB_API без проверки схемы и хоста. FailureExcerpt в :604-636 собирает URL из repo и headSHA через fmt.Sprintf, значения приходят из Watch в :467. getJSON в :639-646 ставит Authorization: Bearer на каждый запрос. headSHA вида abc/../../other меняет путь. Redact в internal/infra/observe/log.go:82-99 не покрывает токены, попавшие в текст ошибки транспорта. Severity high.
Шаги: Проверять базу на https и разрешать только api.github.com плюс loopback для тестов. Кодировать owner, name и headSHA через url.PathEscape или перейти на go-github, как в internal/infra/github/rest.go:308-343. Не прикладывать Bearer к хосту, который не прошёл проверку. Добавить neg тест на подмену базы и на headSHA с разделителями пути.
Проверка: CI зелёный. Тест с BEACON_GITHUB_API на чужом хосте не отправляет запрос. Тест с headSHA, содержащим .., не меняет путь.
Приёмка: Базовый URL проходит проверку до первого запроса. Все параметры пути кодируются.

Итог: Already landed in PR #1220 (commit c19fd7971d3c692b3508cdca67412a4643202e54): GitHub API base restricted and excerpt URL segments escaped; log token redaction covered by PR #1223
Доказательство: PR #1220 merged 2026-11-21T16:27:48Z; commit c19fd7971d3c692b3508cdca67412a4643202e54 changes internal/infra/mcp/tool_get_result.go adding api.github.com host check and url.PathEscape on owner/name/headSHA; git merge-base --is-ancestor c19fd7971d3c692b3508cdca67412a4643202e54 origin/main exit 0; PR #1223 commit ed355f10df703d7dfc8e9aaea43b5b910ee2591f covers token redaction; no open duplicate PR
Причина: Уже влито в PR #1220 c19fd797
Время закрытия: 2026-11-21T17:57:24Z