# AUTOSP-02. Промпт headless-сессии с resume и steering delivery
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/opencode/client.go, internal/infra/opencode/client_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Отправлять первый промпт созданной сессии так, чтобы она продолжила ход после завершения предыдущего раунда.
Контекст: Источник: docs/research/audit-01-autospawn.md, раздел Живой контракт OpenCode. Живой OpenAPI 2.3.2 route session.prompt принимает id, text, files, agents, skills, metadata, delivery и resume. internal/infra/opencode/client.go:370-381 (*Client).Prompt шлёт только text. Транспорт internal/infra/wake/deliver.go уже шлёт resume:true. baseline: prompt_async в живой спецификации встречается 0 раз, флага --attach у opencode run нет.
Шаги: Расширить метод Prompt параметрами messageID, delivery и resume. Вывести messageID детерминированно из obligationID с префиксом msg_, как описано в docs/research/wake-via-prompt-api.md:62. Для стартового промпта слать delivery steer и resume true. Обновить вызов в cmd/beacon/spawn.go через владельца этого файла отдельной волной, в этой карточке менять только клиент и его тесты.
Проверка: CI зелёный. Тест client_test.go проверяет тело запроса с msg_, delivery steer и resume true. Поле agent в body не появляется.
Приёмка: Созданная сессия получает промпт с resume. Повтор того же messageID не создаёт вторую работу.

Итог: Already landed in #1441 (95c5bd42b7e4df8bf0091df2aded8d2b9bc5f711). No duplicate code needed.
Доказательство: PR #1441 commit 95c5bd42b7e4df8bf0091df2aded8d2b9bc5f711 merge-base --is-ancestor 95c5bd42b7e4df8bf0091df2aded8d2b9bc5f711 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1441 95c5bd4
Время закрытия: 2026-11-22T11:29:08Z
