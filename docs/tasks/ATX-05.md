# ATX-05. Заменить сон 250 мс и опрос 1 мс ожиданием счётчика участников
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/tool_wait_result_test.go, internal/infra/store/observability_test.go, internal/infra/mcp/tool_wait_result.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать временные гарантии из тестов ожидания участников и активных ожидающих.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.1 и строка таблицы 153. internal/infra/mcp/tool_wait_result_test.go:1247 ждёт entered.Load(), затем спит 250 мс, чтобы остальные участники присоединились. internal/infra/store/observability_test.go:497 и :502 опрашивает g.ActiveWaiters(key) раз в мс до дедлайта 2 с. Оба теста зависят от планировщика. Baseline: 250 мс сна и 1 мс опроса, дедлайн 2 с.
Шаги: Добавить JoinedWaiters() в WaitResultService. Добавить в SharedGate канал изменения числа ожидающих, либо внедрить часы. Заменить оба цикла и сон ожиданием JoinedWaiters() == units с дедлайтом из t.Context().
Проверка: CI зелёный. В двух тестах нет time.Sleep(250ms) и нет цикла опроса ActiveWaiters через time.Sleep.
Приёмка: Тесты ждут конкретного числа участников. Время ожидания привязано к контексту теста.

Итог: Уже влито в PR #1217
Доказательство: 097ddae4 в origin/main, merge-base --is-ancestor=0