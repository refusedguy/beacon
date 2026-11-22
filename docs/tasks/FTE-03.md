# FTE-03. Открыть снимок SharedGate через beacon_stats
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_stats.go, internal/infra/mcp/tool_stats_test.go
Зависимости: FTE-01
Ссылка: docs/research/research-feature-gaps.md:13,45, docs/runbooks/observability.md:433-449
Агент: ses_dfbf6eb59ffe6Q7CZR8Y5HI0EF
Время: 2026-11-22T16:15:35Z
baseline: git grep SharedCIWaitSnapshot находит только объявление в internal/infra/store/observability.go:786. Потребителя нет, 0 вызовов.
Цель: Оператор видит состояние process-wide CI-гейта в структурированном ответе beacon_stats.
Контекст: PR 1582 добавил SharedCIWaitSnapshot в internal/infra/store/observability.go:786 и отложил подключение к stats. docs/runbooks/observability.md:433-449 перечисляет счётчики, доступные через beacon_stats, но снимка гейта в этом списке нет. Без подключения единственное место, где видно состояние гейта, это лог.
Шаги: Добавить необязательное поле снимка в StatsOutput. Заполнить его из store.SharedCIWaitSnapshot() в композитном корне. Добавить тест с двумя ожидающими на одном ключе. Сохранить обратную совместимость формы ответа при отсутствии снимка.
Проверка: В ответе beacon_stats есть ключи активных ожидающих и числа совмещённых раундов. С двумя ожидающими число ожидающих равно двум, число раундов равно одному. Без ожидающих значения равны нулю.
Приёмка: Снимок доступен через stats и покрыт тестом, CI зелёный.

Причина: Дубль. Открытый PR #1906 (feat(stats): expose shared CI gate snapshot (FTE-03)), ветка fix/fte03, headSHA 44558df18d101e6e8df7d762cb0a1f515fb1a0fd. PR #1906 уже меняет ровно OwnerFiles карточки: internal/infra/mcp/tool_stats.go (+96) и internal/infra/mcp/tool_stats_test.go (+78), плюс docs/tasks/WIRING-1906.md. Решение FTE-03 уже сдано в репозиторий, дубль кода не создаём. Дополнительно: коммит с решением ещё не в origin/main, поэтому merge-base --is-ancestor не применяется; закрытие по факту существующего открытого PR с тем же изменением.
Закрыл: ses_dfbf6eb59ffe6Q7CZR8Y5HI0EF
Время закрытия: 2026-11-22T16:15:35Z
