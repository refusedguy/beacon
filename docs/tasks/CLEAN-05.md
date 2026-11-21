# CLEAN-05. Разобрать 35 дублирующих ADR и ужать docs baseline
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: docs/adrs/, tools/beacon-gates/gate/config/docs-baseline.json
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Удалить дубли ADR, оставив по одному файлу на группу, и убрать соответствующие строки DOC-ADR-DUP из docs baseline с снижением baseline_limit с 40 до 5.
Контекст: Источник: docs/research/cleanup-plan.md, задача 6, и docs/research/deadcode-audit.md, первая строка таблицы. 35 групп, 642 строки, примеры 0069, 0094, 0185, 0260. Гейт держит алфавитно первый файл, docsgate/docs.go:288-300, поэтому удаление без правки baseline словит DOC-BASELINE-STALE. Риск: средний. Нужен owner-decision: сохранять ли историю всех редакций ADR и считать ли канонической алфавитно первую версию.
Шаги: Дождаться решения владельца. Сверить группы дублей по заголовкам и содержимому. Удалить поздние копии, поправить docs/adrs/README.md, убрать 35 строк DOC-ADR-DUP и снизить предел baseline. Изменить файлы и baseline одним PR.
Проверка: informational docs job в CI не показывает DOC-ADR-DUP и DOC-BASELINE-STALE для затронутых путей.
Приёмка: Осталась одна версия каждой ADR, индекс ADR обновлён, baseline_limit снижен до 5. Ряд с owner-decision остаётся todo до ответа владельца.
