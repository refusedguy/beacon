# CMx-08. Интеграционная ветка и отдельный сабагент слияния для spec
Статус: todo
Фаза: P3
Тип: doc
Владелец файлов: docs/design/integration-branch.md
Зависимости:
Ссылка: docs/research/research-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/implement-spec/SKILL.md
Агент: нет
Время: нет
Цель: Добавить в протокол оркестратора правило единой интеграционной ветки и отдельного сабагента слияния для волн из нескольких карточек.
Контекст: Источник: `skills/engineering/implement-spec/SKILL.md:11-19` требует собрать весь spec на одной интеграционной ветке, читать тикеты как task graph с frontier, начинать каждый сабагент со свежего контекста и общаться через context pointers, а не копировать данные. Строки `:27-38`: открыть draft PR после первого влития в интеграционную ветку,каждый implementer подтверждает, что его worktree основан на интеграционной ветке, и подтягивает её перед отчётом, слияние делает отдельный merger-сабагент, новые задачи запускаются при сдвиге frontier, финальный code-review идёт по интеграционной ветке. Строки `:39-42` требуют mark ready и уборку worktree. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'integration branch|merger subagent|интеграционная ветка' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. `docs/templates/orchestrator-task.md:235-248` задаёт заполнение слотов по сигналам, но не оговаривает общую ветку, draft PR и роль merger. Единственные совпадения `draft PR` в `internal/domain/evaluate_pr_test.go:242` и `internal/infra/github/testdata/cases.json:61` относятся к поведению Watch, а не к процессу сборки. research-matt-skills.md счёл этот приём покрытым beacon-cycle, но конкретных артефактов в дереве нет.
Шаги: Создать `docs/design/integration-branch.md`. Описать условия, когда волна требует общей интеграционной ветки, порядок проверки bases worktree перед началом, отдельную роль merger-сабагента и момент открытия draft PR. Записать, что code-review идёт по интеграционной ветке после последнего слияния, а уборка worktree обязательна. Дать указатели на `docs/templates/orchestrator-task.md:235-248` и CM-04 для expand-contract, не переписывая их текст.
Проверка: Файл существует, содержит все пять элементов: общая ветка, проверка bases, merger, draft PR после первого merge, финальный review. `git grep -c 'merger' -- docs/design/integration-branch.md` больше 0. Код не тронут.
Приёмка: Отсутствие отдельной роли merger или условия открытия draft PR проваливает карточку. Измерение до выбора зафиксировано нулевым поиском.
