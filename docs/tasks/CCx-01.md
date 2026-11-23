# CCx-01. Abort-on-drop для dispatch элемента волны
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/orchestrate.go, cmd/beacon/orchestrate_abort_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/core/src/tools/parallel.rs:323
Агент: ses_ccx01_executor
Время: 2026-11-22T16:54:35Z
Цель: Останавливать уже запущенные элементы волны, когда родительский вызов прерывается.
Контекст: Источник: docs/research/comp-codex.md. Codex оборачивает каждый spawn вызова инструмента в `AbortOnDropHandle` (`codex-rs/core/src/tools/parallel.rs:323`). Когда future dispatch исчезает, spawned-задача отменяется. Beacon строит волну как статический план в `cmd/beacon/orchestrate.go:589-623`. У элементов плана нет связанного handle и нет области отмены. Прерванный родитель не может остановить элемент, который уже принят в работу. Baseline: 0 abort handle и 0 областей отмены в `cmd/beacon/orchestrate.go:584-624`.
Шаги: Добавить операцию запуска элемента, которая возвращает handle с отменой при исчезновении родителя. Связать handle с каждым шагом плана. Проверить отмену до запуска и после получения слота. Покрыть тестом на отмену после admission.
Проверка: CI зелёный. Тест с отменой после admission показывает 1 отменённый элемент и 0 элементов, запущенных после отмены.
Приёмка: Изменения только в OwnerFiles карточки. Сигнатура `planWave` и существующие тесты sequential giants остаются рабочими. Код beacon в этом docs-PR не меняется.

Причина: already_landed
Закрыл: beacon
Время закрытия: 2026-11-22T17:01:35Z
## Подтверждение
- PR #1978 MERGED: 2026-11-22 (squash).
