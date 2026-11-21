# ATX-22. Убрать lost update в setEnabled и коллизию ID расписания
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/server/scheduler.go
Зависимости: 
Ссылка: 
Агент: exe97
Время: 2026-11-21T17:54:11Z
Цель: Не терять Pause при параллельном Resume и не сталкивать ID расписаний.
Контекст: Источник: docs/research/concurrency-audit.md, раздел 5 и строка таблицы 141. internal/infra/server/scheduler.go:147, setEnabled читает Get в :152, меняет Enabled и пишет Update в :157 без единой блокировки. buildSchedule в :183 строит ID sched-%d из time.Now().UnixNano() в :185. Два вызова в одну наносекунду дают одинаковый ID и ErrScheduleExists. Приоритет средний для lost update в отчёте.
Шаги: Перенести выбор в store как одну операцию с предикатом. ID брать из atomic counter или случайных байт.
Проверка: CI зелёный. Тест с обёрткой store и параллельными Pause и Resume даёт предсказуемый Enabled. Тест с фиксированными часами и параллельным Schedule не даёт коллизии ID.
Приёмка: Параллельные вызовы не теряют изменение. ID уникален при фиксированном времени.
