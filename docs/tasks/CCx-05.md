# CCx-05. Отложенное одобрение сети с прокси отмены
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/network_approval_test.go
Зависимости: CCx-04
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/orchestrator.rs:65-76, :99-104, :110-124
Агент: ses_ccx05_executor
Время: 2026-11-22T17:13:20Z
Цель: Запрашивать одобрение сети до выполнения и завершать его после результата, не теряя отмену.
Контекст: Источник: docs/research/comp-codex.md. Codex начинает одобрение сети перед попыткой (`codex-rs/core/src/tools/orchestrator.rs:65-76`). В attempt он передаёт токен отмены и прокси выполнения (`:99-104`), а финализацию откладывает до конца запуска (`:110-124`). Beacon не различает сетевые запросы. Политика в `internal/app/orchestrator/orchestrator.go:72-92` описывает только удержание мержа, а изоляция агента это `git clone --reference` (`cmd/beacon/agents.go:406-413`). Baseline: 0 одобрений сети и 0 прокси отмены в пути решения.
Шаги: Добавить спецификацию сети в запрос на выполнение. Начинать одобрение до запуска и связывать его с токеном отмены. Завершать одобрение после результата, включая ошибку. Покрыть тестом на успех, ошибку и отсутствие спецификации.
Проверка: CI зелёный. Тест показывает 1 новое одобрение до запуска, 1 отмену прокси при ошибке и 0 одобрений без спецификации сети.
Приёмка: Изменения только в OwnerFiles карточки. Существующие вердикты Watch не меняются.

Причина: Already landed in PR #1729 (feat(orchestrator): defer network approval with cancel proxy (CCx-05)), squash merge 8f02f459; git merge-base --is-ancestor 8f02f459 origin/main exit 0. No duplicate code PR; docs-only status PR follows.
Закрыл: ses_ccx05_executor
Время закрытия: 2026-11-22T17:13:54Z
