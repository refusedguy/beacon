# BCX-04. Пересобрать ROADMAP_STATUS после исправления merge signal
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: docs/ROADMAP_STATUS.md, tools/roadmap-sync/main.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Привести счётчики и дату в ROADMAP_STATUS к фактическому состоянию main.
Контекст: Источник: docs/research/docs-health-audit.md, разделы 5 и таблица 7 строка 4. Файл печатает 420 done, 332 dropped, 143 todo и дату 2026-11-20. Dry run roadmap-sync даёт 569 done, 326 dropped, 0 todo и предлагает изменить 49 карточек. Расхождение 149 done. Базовый замер и приоритет из отчёта: P1.
Шаги: Дождаться исправления merge signal в BCX-01. Запустить roadmap-sync --write. Генерировать дату снимка из git, а не вручную. Сверить приоритеты P0 3, P1 2, P8 50 с расчётом инструмента.
Проверка: CI зелёный. Повторный dry run roadmap-sync не предлагает изменений. Дата в заголовке совпадает с датой head коммита.
Приёмка: ROADMAP_STATUS PRINT 569 done, 326 dropped, 0 todo на текущем снимке и не расходится со следующим dry run.
