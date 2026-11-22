# AUD-14. Синхронизировать ROADMAP_STATUS с деревом карточек
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: docs/ROADMAP_STATUS.md
Зависимости: 
Ссылка: CI run 44983833077 job 137089954824, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Убрать красный cardindex на main, приведя счётчики в соответствие с docs/tasks.
Контекст: Источник: отчёты агентов PR 1397, 1403, 1415 и 1303. ROADMAP_STATUS.md пишет 665 done, а реально в docs/tasks 764 done / 100 todo после починки парсера ATX-R01 и mergers research-карточек. cardindex на main красный. baseline: расхождение 99 done и 98 todo.
Шаги: Пересобрать счётчики через roadmap-sync --write или beacon index build и записать актуальные значения в docs/ROADMAP_STATUS.md. Проверить, что dry run roadmap-sync не предлагает изменений. Не переводить чужие карточки в done вручную.
Проверка: CI зелёный. Проверка cardindex на main проходит. Пересборка из docs/tasks даёт те же числа, что записаны в ROADMAP_STATUS.md.
Приёмка: Только docs/ROADMAP_STATUS.md. Не трогать файлы из AUD-13.
