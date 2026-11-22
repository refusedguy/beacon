# CCx-17. Ограниченные теги телеметрии проверок
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/observe/otel.go, internal/infra/observe/telemetry_contract_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/exec-server/src/sandbox_integrity/AGENTS.md:23-27
Агент: ses_ccx17b_executor
Время: 2026-11-22T18:17:49Z
Цель: Не пропускать пути, значения окружения и текст ошибки в метрики проверок.
Контекст: Источник: docs/research/comp-codex.md. Codex требует, чтобы телеметрия выводила bounded tags из политики и результата (`codex-rs/exec-server/src/sandbox_integrity/AGENTS.md:23-24`). Пути, значения окружения и текст ошибки не попадают в метрики (`:24-25`). Отсутствие клиента метрик отключает отправку, но не сами проверки (`:25-27`). Beacon записывает текст ошибки напрямую в поля телеметрии (`internal/infra/observe/otel.go:299`). Явного списка допустимых тегов и проверки на длину нет. Baseline: 1 место записи `err.Error()` в телеметрию, 0 белых списков тегов.
Шаги: Определить bounded tag язык для метрик проверок. Добавить фильтр, который отбрасывает путь, окружение и текст ошибки. Проверить, что отсутствие exporter не отключает саму проверку. Покрыть тестом на метрику с конфиденциальным полем.
Проверка: CI зелёный. Тест показывает 0 вхождений `/mnt/`, `PATH=` и текста ошибки в сериализованных метриках. Проверка при отсутствии exporter выполняется 1 раз.
Приёмка: Изменения только в OwnerFiles карточки. Формат существующих JSON-логов в `internal/infra/observe/query_test.go` не менять.

Причина: Already landed in PR #1825 via commit 15885f397ab1e6fd60fb02d673acd8bd4f5de068 (fix(observe): bound check telemetry tags (CCx-17)); git merge-base --is-ancestor 15885f397ab1e6fd60fb02d673acd8bd4f5de068 origin/main exit 0. Fix present in OwnerFiles internal/infra/observe/otel.go and telemetry_contract_test.go. No duplicate code written.
Закрыл: ses_ccx17b_executor
Время закрытия: 2026-11-22T18:18:23Z