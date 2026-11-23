# VCI-05. Подтверждать `merged=true` и сливать проверенный head SHA
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/github/pr_create.go, internal/infra/github/rest.go, internal/infra/github/rest_test.go, cmd/beacon/orchestrate.go
Зависимости: VCI-12, VCI-14
Ссылка: /tmp/opencode/backlog/04-verdict-ci.md, раздел VCI-05; отчёт `/tmp/opencode/reports/04-verdict-ci.md`, PR 1519, 1531, 1581
Агент: ses_df8f88593ffeXjNb9rtlHRGsuj
Время: 2026-11-23
Цель: Слияние идёт только по тому head SHA, который проверил вердикт, и только при подтверждении `merged=true`. Причина отказа видна вызывающему.
Контекст: До починки option merge собирался только с `MergeMethod`, поле `SHA` оставалось пустым. `PRClient.MergePR` выбрасывал ответ go-github, поэтому HTTP 200 с `merged=false` давал nil. `ghErrorFull` обрезал stderr gh до первой строки, где причины отказа нет.
Шаги: Код уже в main, дубль не писать. Зафиксировать закрытие карточки с полными SHA и номерами PR.
Проверка: `git fetch origin main`, затем `git log origin/main --oneline --grep='1519\|1531\|1581' -i` и `git merge-base --is-ancestor <SHA> origin/main` для всех трёх коммитов, exit 0 в каждом случае. Чтение живого кода: `MergeHeldHeadMismatch` и `MergePullRequest` в `internal/infra/github/rest.go:275,286`, запись `expectedSHA` в `opts.SHA` на `:293-296`, разбор 409 и 422 на `:305-313`, отказ при `merged=false` на `:317-319`. `PRClient.MergePR` в `pr_create.go:205`, разбор результата на `:212-228`. `ghErrorFull` и `cliRunner.Run` в `cmd/beacon/orchestrate.go:87,105-114`. Тесты 409 и 422 в `rest_test.go:460-480`.
Приёмка: `MergePullRequest` кладёт проверенный SHA в PUT merge. 409 и 422 при заполненном SHA дают held-причину `head_mismatch` без второй попытки. `merged=false` не считается успехом и даёт reason. `cliRunner.Run` сохраняет все строки stderr gh, однострочный формат лога тика daemon не тронут. `mergePolicyHold` и политика автослияния VCI-12 не меняются, карточка VCI-13 не тронута.

Причина: Already landed in PR #1519 (commit 676bcae601dadac9b7f7a31d95d88928a6b3543f, pin REST merge to the verified head SHA), PR #1531 (commit 5042149eb77c7cfb0327db4050cc1c0b643c7cc0, verify merge confirmation in PRClient.MergePR) and PR #1581 (commit 6086c260c0fe13cc8e70ed70f0b0f1828c2d5324, keep full gh stderr in merge errors). `git merge-base --is-ancestor <SHA> origin/main` exit 0 for all three. Открытого PR с тем же исправлением нет: `gh pr list --state open --search 'VCI-05 OR head_mismatch OR "verified head SHA" in:title'` вернул пустой список.
Ход проверки: Первый прогон CI на PR #2217 уронил обязательную проверку `docs`: таблица команд в README не содержала `skills-lock`. Правка README не входит в OwnerFiles этой карточки. Её внёс PR #2220 (commit 79dc1694), уже влитый в main. Повторный запуск проверки нужен для свежей базы.

Закрыл: ses_df8f88593ffeXjNb9rtlHRGsuj
Время закрытия: 2026-11-23T06:16:14Z
