# ATX-08. Убрать негативную проверку через сон в s2-channel.test.js
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tests/adapter/s2-channel.test.js
Зависимости: 
Ссылка: 
Агент: exe93
Время: 2026-11-21T17:52:14Z
Цель: Заменить проверку на временном окне на проверку лога событий с метками времени.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.6 и строка таблицы 156. tests/adapter/s2-channel.test.js:329 спит 3 * TEST_POLL_MS и затем проверяет, что ничего не доставлено. waitFor в :63 опрашивает предикат каждые 10 мс с лимитом 2000 мс. Негативное утверждение на временном окне хрупкое. Baseline: 3 * TEST_POLL_MS.
Шаги: Внедрить в createS2Channel функции now и tick. Вызывать тики вручную из теста. Негативный случай проверять через лог событий с метками времени, а не через сон.
Проверка: CI зелёный. В s2-channel.test.js нет негативной проверки, основанной на 3 * TEST_POLL_MS.
Приёмка: Тест управляет временем вручную. Отсутствие доставки подтверждается логом событий.

Итог: Fix already landed in main via PR #1213 (commit 2d50ca1be66e462c81f4e9dc483477313861f753), no duplicate code PR needed.
Доказательство: git merge-base --is-ancestor 2d50ca1be66e462c81f4e9dc483477313861f753 origin/main exits 0; PR #1213 https://github.com/refusedguy/beacon/pull/1213
Время закрытия: 2026-11-21T17:54:49Z
