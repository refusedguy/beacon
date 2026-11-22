# AUTOSP-07. Погашение токена слота после проверки артефакта
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/daemon.go, cmd/beacon/daemon_wake_adapter_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Освобождать слот после появления PR или PLAN.md вместо вечного удержания токена.
Контекст: Источник: docs/research/audit-01-autospawn.md, цепочка пункт 3. internal/app/orchestrator/decision.go:629 Burn принимает ArtifactVerifier, но рабочий код его не вызывает. cmd/beacon/daemon.go:158-160 open вызывает только Issue, :167-175 проверяет IsLive. baseline: rg .Burn( по cmd и internal без тестов дал 0 совпадений, значит после первой доставки слот остаётся занятым навсегда.
Шаги: Добавить в daemonWakeGate вызов Burn с ArtifactVerifier, который проверяет наличие PR по watch или PLAN.md по карточке. Вызывать проверку в проходе демона после Refresh. Ошибка проверки оставляет токен живым. Инцидент spawn_without_token писать в observe.
Проверка: CI зелёный. Тест с подтверждённым артефактом снимает слот. Тест с отсутствующим артефактом сохраняет слот. Тест с ошибкой verifier не гасит токен.
Приёмка: Лимит в четыре слота сохраняется в долгом работе демона.

Итог: Already landed in #1439 (ca302a57bb646a4bfb819be320e88ff506c442ad). No duplicate code needed.
Доказательство: PR #1439 commit ca302a57bb646a4bfb819be320e88ff506c442ad merge-base --is-ancestor ca302a57bb646a4bfb819be320e88ff506c442ad origin/main exit 0; no open duplicate PR.
Причина: already landed in #1439 ca302a5
Время закрытия: 2026-11-22T11:29:08Z
