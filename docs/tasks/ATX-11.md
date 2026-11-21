# ATX-11. Ограничить адрес Telegram API и дополнить Redact
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/wake/telegram.go, internal/infra/observe/log.go, internal/infra/store/redaction.go
Зависимости: 
Ссылка: 
Агент: exe94
Время: 2026-11-21T17:55:26Z
Цель: Не отдавать токен Telegram на адрес из env и очищать его во всех фильтрах.
Контекст: Источник: docs/research/security-audit.md, находка 3 и строка таблицы 151. internal/infra/wake/telegram.go:50-58 читает BEACON_TELEGRAM_API, :73-83 принимает любое значение. call в :162-190 собирает endpoint как base + /bot + token + / + method в :167, токен лежит в пути. sanitize в :227-236 вырезает точное значение, но observe.Redact в internal/infra/observe/log.go:82-99 и store.Redact в internal/infra/store/redaction.go:146-166 не знают формат <цифры>:<35 символов>. Severity medium.
Шаги: Ограничить APIBase списком https://api.telegram.org и loopback для тестов. Добавить шаблон токена Telegram в observe.Redact и store.Redact. Хранить токен в заголовке или теле запроса, если протокол позволяет.
Проверка: CI зелёный. Тест на произвольный APIBase отклоняется. Тест Redact очищает токен формата <цифры>:<35 символов> в обоих фильтрах.
Приёмка: Токен не попадает в URL на чужом хосте. Оба фильтра очищают одинаковые форматы.

## Подтверждение
Исправление уже лежит в `origin/main` через PR #1223, коммит ed355f10df703d7dfc8e9aaea43b5b910ee2591f. Коммит меняет ровно OwnerFiles карточки: telegram.go добавляет allowlist APIBase (только https://api.telegram.org и loopback), observe/log.go и store/redaction.go добавляют telegramTokenRE формата `<цифры>:<35 символов>`. Проверка `git merge-base --is-ancestor ed355f10df703d7dfc8e9aaea43b5b910ee2591f origin/main` вернула код 0. Дубль кода не создавался.
