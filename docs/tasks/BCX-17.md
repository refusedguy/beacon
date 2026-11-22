# BCX-17. Собрать имена правил гейтов в одном реестре
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/gate/gate.go, tools/beacon-gates/gate/tool_contract.go, tools/beacon-gates/gate/config/permanent.json, tools/beacon-gates/gate/config/baseline.json
Зависимости: 
Ссылка: ADR 0005
Агент: нет
Время: нет
Цель: Запретить дубли и опечатки в именах правил гейтов.
Контекст: Источник: docs/research/error-codes-audit.md, P1 «Правила гейтов не собраны в одном месте». Имена объявлены четырьмя способами: константы в tool_contract.go, reachability.go, wiring.go; inline-строки в docsgate/docs.go и gate/layers.go; динамическая сборка CAP-* + -IN- + layer в capabilities/capabilities.go:95; ключи в gate/config/*.json. gate.Finding.Rule в gate.go:23 это просто string. Базовый замер и приоритет из отчёта: P1.
Шаги: Собрать все имена в один реестр или добавить проверку уникальности и известности на записи Finding.Rule. Перевести inline-строки и ключи конфигов на реестр. Добавить тест на неизвестное имя правила.
Проверка: CI зелёный. Тест отвергает неизвестное или повторное имя правила. Конфиги используют только имена из реестра.
Приёмка: Новое правило гейта нельзя завести опечаткой. Все способа объявления дают один список имён.

Итог: Already landed in PR #1405 (commit 823d852b4569ea208a0257d4b5a2d87a08b67646). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1405 merged 2026-11-22T10:32:13+05:00; commit 823d852b4569ea208a0257d4b5a2d87a08b67646; git merge-base --is-ancestor 823d852b4569ea208a0257d4b5a2d87a08b67646 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1405 823d852b
Время закрытия: 2026-11-22T11:24:28Z
