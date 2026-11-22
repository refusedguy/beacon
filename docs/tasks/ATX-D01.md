# ATX-D01. Закрепить отсутствие дублей номеров ADR
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/docsgate/docs.go, tools/beacon-gates/gate/config/docs-baseline.json
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не допустить возврата дублей номеров ADR после PR 1264.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D01. baseline на a7dcb6f: 305 файлов ADR, 305 уникальных номеров, 0 дублей, диапазон 0001-0305 без пропусков. PR 1264 (bf4a08ec, BCX-02) убрал 24 дубля, PR 1261 (e5f3a22c, BCX-06) переименовал 66 untitled-файлов. В `docs-baseline.json` записаны `baseline: []` и `baseline_limit: 40`. Правило `DOC-ADR-DUP` в gate есть, но проверки на текущем дереве с требованием нуля дублей нет.
Шаги: Добавить в docsgate self-test или отдельный тест, который группирует файлы по номеру и требует ровно один файл на номер. Держать baseline пустым. Добавить комментарий в конфиг о том, почему исключений нет.
Проверка: CI зелёный. self-test docsgate проходит. На дереве с искусственным вторым файлом того же номера тест падает с `DOC-ADR-DUP`.
Приёмка: Повтор номера ловится до мержа. Число строк baseline не растёт.

Итог: Already landed in PR #1321 (commit aed261234e1ee143488e17241c9700d3d196d054): docs(research): ADR and docs index consistency gaps (ATX-D01..D08) (#1321)
Доказательство: PR #1321; commit aed261234e1ee143488e17241c9700d3d196d054; git merge-base --is-ancestor aed261234e1ee143488e17241c9700d3d196d054 origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1321 aed261234e1e
Время закрытия: 2026-11-22T11:27:01Z
