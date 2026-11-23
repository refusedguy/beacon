# CMx-12. Визард для шагов, которые может выполнить только человек
Статус: done
Фаза: P3
Тип: doc
Владелец файлов: docs/runbooks/human-wizard.md
Зависимости:
Ссылка: docs/research/comp-matt-skills.md; mattpocock/skills@abeeaf9 skills/engineering/wizard/SKILL.md
Агент: ses_cmx12b_executor
Время: 2026-11-22T18:10:08Z
Цель: Описать, когда beacon использует bash-визард для ручных шагов владельца и какие требования этот скрипт обязан соблюдать.
Контекст: Источник: `skills/engineering/wizard/SKILL.md:3-8` ограничивает применение шагами человека: provision инфраструктуры, создание учётных данных и секретов CI, работа в незнакомой панели, разовый cutover. Если агент может сделать шаг сам, визард не нужен. Строки `:16-31` требуют прочитать репозиторий до расспроса, перечислить этапы и значения, для каждого значения знать источник, место записи и секретность, а также доводит этапы до конкретных инструкций. Строки `:33-44` требуют скрытый ввод секретов, идемпотентную запись `.env`, `gh secret` и `gh variable`, подтверждение перед действием, которое нельзя откатить, проверку `bash -n` и shellcheck и статический trace вместо end-to-end прогона. baseline по `origin/main` `0ce4ba29`: `git grep -i -c -E 'wizard|ask_secret|write_env|TOTAL_STAGES' -- AGENTS.md README.md GLOSSARY.md docs/templates .opencode docs/design docs/adrs internal cmd tools scripts` даёт 0. В `README.md:314` описан флаг `BEACON_ALLOW_MAIN_COMMIT` для разработчика, но нет единого порядка шагов человека. CMx-13 описывает handoff-документ, а не интерактивный скрипт.
Шаги: Создать `docs/runbooks/human-wizard.md`. Записать порог применения: только шаг, который агент не может выполнить сам. Перечислить обязательные поля этапа: название, источник значения, место записи, секретность. Зафиксировать требования к скрипту: скрытый ввод, идемпотентная запись, подтверждение перед действием, которое нельзя откатить, `bash -n`, shellcheck и статический trace. Указать, что визард по умолчанию временный и коммитится только как повторяемый путь настройки.
Проверка: Файл существует, содержит порог применения, четыре поля этапа и все пять требований к скрипту. `git grep -c 'ask_secret' -- docs/runbooks/human-wizard.md` больше 0. Код не тронут.
Приёмка: Отсутствие порога применения или требования idempotent записи проваливает карточку. Измерение до выбора зафиксировано нулевым поиском по четырём терминам.

Причина: Fix already landed in main via PR #1849 (commit eda4eaf6590cbce3df05174131676573c427fd66, docs(runbooks): add human wizard runbook (CMx-12)); git merge-base --is-ancestor eda4eaf6590cbce3df05174131676573c427fd66 origin/main exit 0. Runbook docs/runbooks/human-wizard.md present in main with threshold, four stage fields and five script requirements. No duplicate runbook edit.
Закрыл: ses_cmx12b_executor
Время закрытия: 2026-11-22T18:11:32Z
## Подтверждение
- PR #2092 MERGED: 2026-11-22 (squash).

