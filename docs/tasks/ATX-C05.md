# ATX-C05. Проверять поле OwnerFiles в формате карточек инструмента
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/gate/config/baseline.json, tools/beacon-gates/gate/wiring.go, tools/beacon-gates/gate/wiring_test.go
Зависимости: 
Ссылка: docs/research/ci-gates-gaps.md, находка C05
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Сделать wiring-гейт совместимым с карточками, которые пишет beacon_defer_wiring.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C05. Замер: 2 из 13 карточек `docs/tasks/WIRING-*.md` не содержат `OwnerFiles:`, это WIRING-1106 и WIRING-919. `tools/beacon-gates/gate/config/baseline.json:10` задаёт `owner_field: "OwnerFiles"`, а `internal/infra/mcp/tool_defer_wiring.go:153` пишет только `Владелец файлов:`. Шаблон `docs/templates/wiring-card.md` дублирует оба поля. Фикстуры в `tools/beacon-gates/gate/wiring.go:181-194` и `wiring_test.go` используют только `OwnerFiles:`, поэтому self-test проходит при расхождении с рабочим деревом.
Шаги: Поддержать оба имени поля в `CheckWiring`: сначала читать configured поле, при пустом значении читать `Владелец файлов`. Либо перевести конфиг на русское имя и обновить шаблон и фикстуры. Добавить тест на карточку в формате `tool_defer_wiring` и тест на карточку из шаблона. Сохранить rule-имена WIRING-STATUS, WIRING-OWNERFILES и WIRING-REASON.
Проверка: CI зелёный. Тест с карточкой, содержащей только `Владелец файлов:`, не даёт WIRING-OWNERFILES. Тест с пустыми обоими полями даёт finding. Self-test wiring проходит на обеих формах карточки.
Приёмка: Гейт принимает карточки из инструмента и из шаблона. Расхождение имени поля больше не скрывается self-test.

Причина: Already landed in PR #1636 (commit 6594759e668f7edeef4a6577d3170c12ca26a7af): fix(gates): accept Владелец файлов in wiring cards. git merge-base --is-ancestor 6594759e668f origin/main exit 0.
Закрыл: ses_dfca4cc64fferoguUmyJU12sVW
Время закрытия: 2026-11-22T13:10:50+00:00
