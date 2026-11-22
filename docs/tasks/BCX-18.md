# BCX-18. Убрать дубль NormalizeConclusion между domain и app
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/domain/verdict.go, internal/app/orchestrator/checkall.go, internal/infra/github/batch_provider.go, internal/infra/cibridge/bridge.go
Зависимости: 
Ссылка: ADR 0005
Агент: нет
Время: нет
Цель: Использовать одну нормализацию списков PASS и FAIL во всех слоях.
Контекст: Источник: docs/research/error-codes-audit.md, P2 «Две копии функции нормализации». domain/verdict.go:244-256 NormalizeConclusion и app/orchestrator/checkall.go:703-713 normalizeConclusion содержат одинаковые списки PASS/SUCCESS/SUCCESSFUL/SKIPPED/NEUTRAL и FAIL/FAILURE/ERROR/TIMED_OUT/ACTION_REQUIRED. Частичные копии в github/batch_provider.go:597 и cibridge/bridge.go:616. Базовый замер и приоритет из отчёта: P2.
Шаги: Оставить реализацию в domain и вызывать её из app. Частичные копии заменить маппингом к общему результату. Сохранить осознанную нормализацию CANCELLED и CANCELED.
Проверка: CI зелёный. Тесты на всех входных значениях проходят через общую функцию. Дубли удалены.
Приёмка: Изменение списка нормализации в одном месте меняет поведение всех потребителей.

Итог: Already landed in PR #1415 (commit 3137a74c601d55e8634efd99274749b5f1ab26d3). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1415 merged 2026-11-22T09:42:48+05:00; commit 3137a74c601d55e8634efd99274749b5f1ab26d3; git merge-base --is-ancestor 3137a74c601d55e8634efd99274749b5f1ab26d3 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1415 3137a74c
Время закрытия: 2026-11-22T11:24:28Z
