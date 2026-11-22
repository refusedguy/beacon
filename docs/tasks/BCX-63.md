# BCX-63. Разрешить или запретить свой TUI и снять дубли proposed ADR
Статус: done
Фаза: P0
Тип: task
Владелец файлов: docs/adrs/0060-cli-adapter-boundary-and-live-loop.md, internal/tui/app.go, tools/beacon-gates/gate/config/layers.json, cmd/beacon/main.go
Зависимости: 
Ссылка: ADR 0060
Агент: нет
Время: нет
Цель: Снять противоречие между запретом ADR 0060 и наличием кода TUI в main.
Контекст: Источник: docs/research/adr-consistency-audit.md, таблица строка 2. ADR 0060 прямо пишет «Свой TUI или веб-интерфейс не создаётся». В main есть internal/tui/app.go, client.go, socket.go. Ребро cmd к internal/tui запрещено в layers.json:17-22, dispatch board отсутствует. Восемь ADR про TUI со статусом proposed дублируют два текста. WIRING-493 и WIRING-915 требуют снять ограничения; коммит f5b1d7d4 не предок main. Базовый замер и приоритет из отчёта: P0.
Шаги: Проверить premise на текущем main. При разрешении снять запрет в ADR 0060, закрыть дубли proposed ADR, подключить WIRING-493 и WIRING-915 и разрешить ребро в layers.json. При запрете удалить internal/tui и закрыть обе wiring-карточки как dropped.
Проверка: CI зелёный. Код и ADR согласованы. Дубли proposed ADR закрыты или объединены. Ребро в layers.json соответствует выбору.
Приёмка: Решение по TUI зафиксировано и не оставляет мёртвый код или запрещённое ребро.

Итог: Already landed in PR #1328 (commit 3607970ecc343f8e6740a11070340f4bafec79ed). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1328 merged 2026-11-21T22:52:51+05:00; commit 3607970ecc343f8e6740a11070340f4bafec79ed; git merge-base --is-ancestor 3607970ecc343f8e6740a11070340f4bafec79ed origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1328 3607970e
Время закрытия: 2026-11-22T11:24:28Z
