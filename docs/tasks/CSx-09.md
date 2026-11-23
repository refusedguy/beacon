# CSx-09. Контракт соответствия для адаптеров хранилища
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/events_test.go, internal/infra/store/sessions.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: ses_csx09_executor
Время: 2026-11-22T17:41:12Z
Цель: Проверять адаптеры хранилища beacon одним общим набором контрактов, а не наборами точечных тестов.
Контекст: SDK поставляет `run_session_store_conformance`, который проверяет 14 поведенческих контрактов адаптера и пропускает необязательные методы через `skip_optional` (`src/claude_agent_sdk/testing/session_store_conformance.py:1-15`, `:47-60`). В beacon тесты открывают конкретную SQLite базу через `openEventStore` (`internal/infra/store/events_test.go:11-19`), а операции над сессиями привязаны к конкретному `DB` (`internal/infra/store/sessions.go:53-64`). Общего набора контрактов для адаптеров нет. CS-04 описывает материализацию сессии из store, но не описывает проверку самого адаптера.
baseline: `rg -n 'conformance' internal cmd tests .opencode` на `origin/main` 7244e15b даёт 0 строк; `rg -c '^func Test' internal/infra/store/events_test.go` даёт 6 тестов, каждый из которых работает с конкретным `DB`.
Шаги: Выделить минимальный порт хранилища из `DB`. Написать переиспользуемую функцию прогона контрактов, которая получает фабрику хранилища и список пропускаемых необязательных методов. Покрыть порядок записей, идемпотентность по ключу, чтение, удаление и перечисление. Прогнать набор минимум на двух реализациях.
Проверка: Тест прогоняет набор на двух адаптерах, оба проходят все контракты. Специально сломанный адаптер проваливает ровно один контракт. Измеримая проверка: число именованных контрактов в наборе не меньше 14; `rg -n 'runStoreConformance|skipOptional' <ветка> -- internal/infra/store` даёт не менее 2 строк.
Приёмка: Новый адаптер хранилища проходит один и тот же набор проверок без копирования чужих тестов.

Причина: Already landed in PR #1761 (merge commit 330dd95a4cbf93d56982dfb1dc1e1146c1b84e05); git merge-base --is-ancestor 330dd95a origin/main exit 0. Fix touches both OwnerFiles: internal/infra/store/events_test.go and internal/infra/store/sessions.go. No duplicate code PR; this PR carries only the card close with evidence.
Закрыл: ses_csx09_executor
Время закрытия: 2026-11-22T17:41:53Z
## Подтверждение
- PR #2054 MERGED: 2026-11-22 (squash).

