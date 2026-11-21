# CLEAN-01. Пересчитать счётчики roadmap по факту статусов карточек
Статус: done
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

## Подтверждение

- Грамматика `CLEAN-[0-9]+` и `ARDH-[0-9]+` и тест `TestIDPatternParsesCleanAndArdhCards` уже лежат в базовой ветке после PR #1152. Дубль не добавлял, в README перечислил оба семейства среди распознаваемых.
- baseline: на коммите 5772223 `git grep -h '^Статус:' -- docs/tasks` дал done 644, dropped 326, todo 48, in_progress 2. Dry-run `tools/roadmap-sync` до правки дал done 646, dropped 326, todo 20, P0 1, P1 4, P2 15.
- Счётчики записаны через `tools/roadmap-sync --write`: done 647, dropped 326, todo 19, P1 4, P2 15. Повторный dry-run после записи печатает 0 изменений в строках счётчиков и приоритетов.
- Статусы других карточек не менял. Dry-run предлагает переводы для T3.241, T3.243, T5.49, T5.52 и группы ARDH/CLEAN по совпадению OwnerFiles, но это заблокированные карточки и чужие OwnerFiles, вне объёма этой задачи.
- Риск низкий, правки только в docs и README по задаче 2 из `docs/research/cleanup-plan.md`. Решение владельца не требовалось.
