# ATX-40. Добавить курсор и признак лимита в HistorySearch
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/wake/deliver.go
Зависимости: 
Ссылка: 
Агент: exe115
Время: 2026-11-21T17:58:06Z
Цель: Дать вызывающему продолжить поиск после обрезания.
Контекст: Источник: docs/research/output-limits-audit.md, раздел 5 и строка таблицы 175. internal/infra/wake/deliver.go:1576-1579 и :1583 задают historySearchMaxLines 200 и historySearchMaxBytes 16 KiB. clampHistoryOutput в :1615-1667 оставляет первые 200 строк и укладывается в 16 KiB. Единственный явный сигнал об обрезании во всём аудите: Truncated = true. Курсора нет, какое именно ограничение сработало, не сообщается. Baseline: 200 строк и 16 KiB.
Шаги: Вернуть nextOffset или указать, какой лимит сработал. Оставить Truncated = true для обратной совместимости.
Проверка: CI зелёный. Тест с 201 строкой возвращает nextOffset и Truncated. Повторный запрос с nextOffset даёт следующую строку.
Приёмка: Вызывающий может продолжить поиск без чтения файла напрямую. Причина обрезания видна.

Итог: Уже реализовано в PR #1254, коммит af34998b91c059464c9d9f07cf275eaa6e0bba7f. HistorySearchResult получил NextCursor, HistorySearchPage принимает курсор, Truncated сохранён для обратной совместимости.
Доказательство: PR #1254 merged 2026-11-21T16:47:45Z; коммит af34998b91c059464c9d9f07cf275eaa6e0bba7f меняет internal/infra/wake/deliver.go; git merge-base --is-ancestor af34998b91c059464c9d9f07cf275eaa6e0bba7f origin/main exit 0; открытых PR-дублей нет.
Причина: already landed in PR #1254 af34998b
Время закрытия: 2026-11-21T18:02:59Z
