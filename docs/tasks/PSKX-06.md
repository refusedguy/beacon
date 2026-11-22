# PSKX-06. Карта пользовательских путей в e2e-прогоне
Статус: done
Фаза: P8
Тип: doc
Владелец файлов: docs/e2e/README.md
Зависимости: 
Ссылка: https://raw.githubusercontent.com/cursor/plugins/main/pstack/skills/create-verification-skill/SKILL.md
Агент: ses_pskx06_executor
Время: 2026-11-22T17:19:11Z
Цель: Иметь в репозитории карту пользовательских возможностей: по файлу на возможность с путём доступа, командами управления и наблюдаемым конечным состоянием; проверять реальный путь, а не внутренние сеттеры.
Контекст: Источник: `pstack/skills/create-verification-skill/SKILL.md:27-36` (feature map). `docs/e2e/README.md:30-38` описывает инструменты и отдельные проверяемые факты, но не содержит карты возможностей. Нет файлов `features/` и правила, что прогон, который трассирует только один удобный вход, неполон. baseline: `git grep -i -E 'карта возможностей|features/README' -- docs/e2e/README.md | wc -l` = 0 на `origin/main`. Карточка фиксирует приём в дереве beacon, не создаёт новый глобальный скилл и не меняет протокол Watch. Не дублирует PSK-01 (кандидаты), PSK-02 (адверсарии), PSK-03 (TLA Watch), CP-01..05, CM-01..05 или закрытые дубли HD-099/HD-105/HD-109. Риск: низкий.
Шаги: Создать `tests/e2e/features/README.md` и по файлу на пользовательскую возможность beacon:Watch, claim карточки, wake сессии, мерж по `ready`. В каждом файле записать путь доступа, команды прогона и наблюдаемое состояние. Добавить в `docs/e2e/README.md` правило: прогон считается полным, когда каждая возможность из карты была проверена живым запуском, а не только через внутренний вызов.
Проверка: `git grep -n -i -E 'карта возможностей|features/README' docs/e2e/README.md` находит правило. `git ls-tree -r --name-only HEAD -- tests/e2e/features/` показывает не менее четырёх файлов.
Приёмка: Карта содержит все перечисленные возможности. Отсутствие возможности в карте obstructionирует прогон, а не упрощает его.

Причина: Implementation already in origin/main. Rule "Карта возможностей и полнота прогона" added to docs/e2e/README.md by merged PR #1744, commit 938b266a6063b61872eab1d9e04306884e87a269. git merge-base --is-ancestor 938b266a origin/main exit=0. No duplicate code written.
Закрыл: ses_pskx06_executor
Время закрытия: 2026-11-22T17:20:04Z
