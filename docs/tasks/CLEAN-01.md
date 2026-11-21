# CLEAN-01. Пересчитать счётчики roadmap по факту статусов карточек
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: docs/ROADMAP_STATUS.md, tools/roadmap-sync/main.go, tools/roadmap-sync/README.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Привести числа done, dropped, todo и строки приоритетов в docs/ROADMAP_STATUS.md к фактическим статусам карточек и расширить грамматику ID в tools/roadmap-sync новыми семействами CLEAN и ARDH.
Контекст: Источник: docs/research/cleanup-plan.md, задача 2. Зафиксировано расхождение 420 / 332 / 143 против 539 done, 326 dropped, 66 todo в 931 карточке. tools/roadmap-sync/README.md описывает --git-log и --write. Риск: низкий. Docs job informational, .github/workflows/docs.yml:22, ci.yml игнорирует docs/**.
Шаги: Добавить CLEAN-[0-9]+ и ARDH-[0-9]+ в idPattern tools/roadmap-sync/main.go и тест на новые имена файлов. Прогнать инструмент в dry-run и сверить итоговые числа. Применить изменение только к строкам счётчиков и приоритетов ROADMAP_STATUS.md, статусы чужих карточек не трогать.
Проверка: Повторный dry-run roadmap-sync не показывает изменений в строках счётчиков и приоритетов.
Приёмка: Числа и приоритеты совпадают с подсчётом карт docs/tasks/*.md, включая карточки CLEAN и ARDH. Статусы существующих карточек не перезаписаны.
