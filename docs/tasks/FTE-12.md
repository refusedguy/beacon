# FTE-12. Добавить команду beacon trace
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/trace.go, cmd/beacon/trace_test.go, cmd/beacon/catalog.go, cmd/beacon/main.go
Зависимости:
Ссылка: docs/evals/e07-trace-session-last-ten-minutes.md:858-868, README.md:223
Агент: ses_e07974f3afferWqmGMxXPudXrj
Время: 2026-11-22T16:40:13Z
baseline: cmd/beacon/catalog.go:33-62 и cmd/beacon/main.go:253-286 не содержат trace. git grep по cmd/beacon не находит функции трассировки сессии, 0 совпадений.
Цель: Оператор получает журнал wake, ack и retry одной сессии за последние десять минут через beacon trace.
Контекст: docs/evals/e07-trace-session-last-ten-minutes.md:862 обещает beacon trace --session ses_test --since 10m с wake, ack и retry и их источниками. Строка 863 требует в каждой строке at, watchID, revision и reason. Строка 864 требует no evidence при отсутствии фактов. README.md:223 перечисляет команды диагностики, trace в этом списке нет. Журнал событий в store есть, команда доступа к нему отсутствует.
Шаги: Добавить команду trace с флагами --session и --since. Читать события из store по сессии и интервалу. Форматировать строки с at, watchID, revision и reason. Поддержать table и json через общий парсер форматов. Не выводить выдуманные причины. Добавить тест на пустой результат и на смешанный набор событий.
Проверка: beacon trace --session ses_test --since 10m возвращает код 0 и содержит wake, ack и retry с источниками. Каждая строка несёт четыре обязательных поля. Для неизвестной сессии команда печатает no evidence. Подмена причины в выводе падает тестом.
Приёмка: Команда описана в каталоге, eval e07 проходит, CI зелёный.

Причина: Already landed in main: commit 5a17773d9c5fce8ee1e84fc01ff9d034375982bf (PR #1810) added cmd/beacon/trace.go and cmd/beacon/trace_test.go with 429 lines. git merge-base --is-ancestor 5a17773d origin/main returned exit 0. No duplicate code needed; this PR carries only the card status.
Закрыл: ses_e07974f3afferWqmGMxXPudXrj
Время закрытия: 2026-11-22T16:40:21Z
