# BP-03: отложить до влития PR #2230

Режим документа: explanation. Здесь зафиксировано решение отложить карточку BP-03 и условие её пересмотра. Код, карточки в `docs/tasks/`, гейты и конфиг этим документом не меняются.

## Решение

Оркестратор решил отложить BP-03 до влития PR #2230 (`fix(cibridge): give reopened blocker incidents a fresh wake key`, VCI-02). BP-03 требует правки classification в `internal/infra/cibridge/verdict_applier.go`, а PR #2230 меняет тот же файл и остаётся открытым. Параллельный diff даст конфликт на следующем ребейзе и усугубит проблему исчерпания бюджета ребейзов, описанную в `docs/runbooks/REBASE-BUDGET.md` (PR #2236).

## Проверенные факты

Проверка по `origin/main` 79dc1694, повторена на e589b3e4. Факты совпадают: PR #2230 и #2236 остаются открытыми.

- BP-03 в main не влит, `deliverWithOutcome` в дереве отсутствует.
- Вызовы `isDeadSessionErr`: `verdict_applier.go:691`, `:874`, `wake_dispatcher.go:438`, `:837`, `:942`. Определение в `wake_dispatcher.go:1082`. В брифе исполнителя указаны строки 690, 873, 438, 836, 941. Проверка по `origin/main` даёт сдвиг на одну строку для четырёх из пяти вызовов.
- PR #2230 добавляет `blockerWakeDedup` и меняет `verdict_applier.go` около строк 766 и 951. Оба места лежат в файле, который BP-03 должен править для classification.
- OwnerFiles `wake_dispatcher.go` и `bridge.go` свободны, но acceptance BP-03 требует правки `verdict_applier.go`. Переехать в другой файл, чтобы обойти конфликт, нельзя.

Вывод агента: PR #2230 открыт, headSHA `c2938468c455c8484d5aa03f20d2aa1c5d6a3e7f`, `mergeable=MERGEABLE`, `mergeStateStatus=UNSTABLE`. PR #2236 открыт, headSHA `da0aef3c17be204c0289c03ebef6df1fed91fe8c`, и вводит `docs/runbooks/REBASE-BUDGET.md`. На main этот runbook пока отсутствует.

## Связь с бюджетом ребейзов

Источник: `docs/runbooks/REBASE-BUDGET.md` (PR #2236). Runbook описывает состояние Watch с `reason=rebase_budget_exhausted`, `blockedBy=cibridge`, `maxRebases=3`. Каждый ребейз списывается в постоянное обязательство `(watchID, revision, rebase-counter)` и переживает перезапуск процесса. Повтор проверок на устаревшей базе не меняет базу и не возвращает потраченные попытки.

Вывод агента: если открыть BP-03 сейчас, его база получит `verdict_applier.go` до влития #2230. После мержа #2230 verifier запросит ребейз, и BP-03 потратит попытку на разрешение конфликта в том же файле. При бюджете три попытки могут закончиться до первой проверки classification в CI. Отложенный старт не расходует бюджет на чужой конфликт.

## Условие пересмотра

Пересмотреть BP-03 после влития PR #2230 в main.

1. Выполнить `git fetch origin main`, взять свежий `origin/main` и новую ветку от него.
2. Перепроверить classification: вызовы `isDeadSessionErr` в `verdict_applier.go` и `wake_dispatcher.go`, наличие `deliverWithOutcome`, новую нумерацию строк после правок #2230.
3. Сверить acceptance BP-03 с обновлённым файлом. Если правка по-прежнему требует `verdict_applier.go`, конфликт с #2230 ушёл и карточку можно брать в работу.
4. Если к тому времени открыт новый PR, меняющий тот же файл, повторить оценку по этой же схеме.

## Граница

Только документация. Локальные `go test`, `go build` и `beacon gates` не запускались, проверки прогоняет CI на PR.
