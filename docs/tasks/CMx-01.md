# CMx-01. Двухосевой разбор diff как контракт проекта
Статус: done
Фаза: P2
Тип: doc
Владелец файлов: docs/design/review-axes.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; docs/research/research-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/code-review/SKILL.md
Агент: ses_cmx01_executor
Время: 2026-11-22T16:54:41Z
Цель: Зафиксировать в дереве beacon двухосевой разбор diff (Standards и Spec), выполняемый параллельными сабагентами с фиксированным набором запахов, а не только глобально установленный скилл.
Контекст: Источник: `skills/engineering/code-review/SKILL.md:6-12` задаёт две оси Standards и Spec и требует запускать их параллельными сабагентами в свежих окнах контекста, чтобы они не портили друг другу контекст. Строки `:34-41` предписывают искать все документированные стандарты и всегда нести smell baseline из двенадцати запахов Fowler, при этом документированный стандарт репо перекрывает baseline. Строки `:43-56` перечисляют запахи по имени: Mysterious Name, Duplicated Code, Feature Envy, Data Clumps, Primitive Obsession, Repeated Switches, Shotgun Surgery, Divergent Change, Speculative Generality, Message Chains, Middle Man, Refused Bequest. Строки `:58-80` требуют отдельные промпты для двух сабагентов, сводку под заголовками `## Standards` и `## Spec` и запрет переранжировать находки в одного победителя. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'two-axis|Fowler|Feature Envy' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. В `AGENTS.md:113-115` есть только сопоставление `aif-review` с локальными `code-review` и `pr`, а в `README.md:339-348` таблица функциональных аналогов. Ни один активный артефакт не задаёт порядок разбора, букваря запахов и запрета смешивать оси. Это не дубль HD-106: та карточка закрыла саму таблицу аналогов, а не контракт разбора.
Шаги: Создать `docs/design/review-axes.md`. Описать две оси и требование двух параллельных сабагентов со свежим контекстом. Привести полный список из двенадцати запахов с правилом «стандарт репо перекрывает baseline» и «запах это эвристика, а не жёсткое нарушение». Зафиксировать сводку под двумя заголовками и запрет выбирать одного победителя между осями. Дать указатели на `AGENTS.md:113-115` и шаблоны в `docs/templates/`, не переписывая их.
Проверка: Файл существует, в нём названы обе оси, все двенадцать запахов, параллельные сабагенты и запрет переранжирования. `git grep -i -c 'Feature Envy' -- docs/design/review-axes.md` больше 0. `git grep -i -c -E 'two-axis|two axes' -- AGENTS.md docs/design` больше 0 после влития. Код и ADR не тронуты.
Приёмка: Отсутствие любой из двенадцати запахов, одной оси или правила раздельной сводки проваливает карточку. Измерение до выбора зафиксировано тремя нулевыми поисками.

Причина: Решение уже влито в origin/main: PR #1746, squash-коммит ebd98317. git merge-base --is-ancestor ebd98317 origin/main вернул 0. Файл docs/design/review-axes.md уже содержит обе оси, все двенадцать запахов и запрет переранжирования. Дубль кода не создан.
Закрыл: ses_cmx01_executor
Время закрытия: 2026-11-22T16:55:04Z
