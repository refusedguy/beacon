# ATX-25. Сделать интервал повтора замка внедряемым в taskcli
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/taskcli/service.go, cmd/beacon/main_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сократить 8 с тестов taskcli без ослабления проверки замка.
Контекст: Источник: docs/research/ci-speed-audit.md, раздел test и строка таблицы 91. В прогоне 44913098256 cmd/beacon занимает 9,8 с, internal/app/taskcli 8,0 с, internal/infra/mcp 2,9 с. В taskcli два теста по 20 раз устраивают гонку за файловый замок с шагом 100 мс, итого 40 повторов. Гипотеза про 4-5 с не подтверждена замерами с -json. В отчёте нет метки P0/P1, приоритет P1 взят из оценки риска Средний.
Шаги: Сделать интервал повтора замка внедряемым и передавать 1 мс в тестах. Добавить t.Parallel только к независимым тестам. Не трогать property-тесты с flag.Set.
Проверка: После мержа сравнить медиану времени джобы test по 10 прогонам. Проверить, что тест всё ещё ловит гонку за замок.
Приёмка: Время taskcli снижено. Проверка конкуренции за замок сохранена.

Итог: Already landed in PR #1374 (commit 2e7b2bfaac04623a5e9054078a50bd8a12a94c75): fix(taskcli): injectable lock retry interval (ATX-25) (#1374)
Доказательство: PR #1374; commit 2e7b2bfaac04623a5e9054078a50bd8a12a94c75; git merge-base --is-ancestor 2e7b2bfaac04623a5e9054078a50bd8a12a94c75 origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1374 2e7b2bfaac04
Время закрытия: 2026-11-22T11:27:01Z
