# ADR 198: merge.driver с проверкой expectedHeadOid

Статус: proposed

## Контекст

GitHub auto-merge снимает часть опросов, но не выражает условия Watch.

## Решение

merge.driver = beacon по умолчанию, github-auto-merge только для PR без waitFor и waitForMain. Мерж передаёт expected head OID.

## Последствия

Гибрид сохраняет контракт ADR 0072. Требуется конфиг проекта и ошибка при конфликте условий.
