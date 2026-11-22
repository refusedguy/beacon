# TUI-02. Собрать единую Bubble Tea модель по снимку и карточкам
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/tui/app.go, internal/tui/board.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Показать в одном окне вотчи с вердиктами, воркеров и фазы, блокеры, очередь, хвост событий, RSS и GitHub points.
Контекст: Источник: docs/research/audit-03-tui.md, таблица строки 2. internal/tui/app.go:18 App хранит Snapshot и кэширует рендер в методе View на строке 68. internal/tui/app.go:125 Run печатает текст в io.Writer. internal/tui/board.go:103 Model рисует только карточки, а board.go:342 RenderFallback печатает таблицу. Модели не объединяют живой снимок и задачи.
Шаги: Передать Snapshot из App в Bubble Tea модель как новые панели. Сохранить метод View как запасной текстовый рендер для вывода без TTY. Показывать revision и последнюю ошибку обновления. Ограничить высоту списков по WindowSizeMsg из board.go:202. Поддержать команды из app.go:100 Command как действия модели. Не менять контракт Card из board.go:20.
Проверка: CI зелёный. Первый кадр содержит все панели из цели. Повторное событие меняет экран только при nuevo revision. Тест модели проверяет выбор вотча и отказ команды.
Приёмка: Один экран заменяет два отдельных просмотра. Текстовый fallback печатает те же данные без терминала.

Итог: Already landed in PR #1446 (commit ce5df17be03ad85b332170bdcf1b18d636fe273d). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1446 merged 2026-11-22T09:59:08+05:00; commit ce5df17be03ad85b332170bdcf1b18d636fe273d; git merge-base --is-ancestor ce5df17be03ad85b332170bdcf1b18d636fe273d origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1446 ce5df17b
Время закрытия: 2026-11-22T11:25:31Z
