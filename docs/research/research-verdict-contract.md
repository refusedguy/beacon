# Домен вердиктов и переходы состояний: остатки контракта

Дата: 2026-11-22. Режим: только чтение. `go test`, `go build`, гейты и `batch-apply` не запускались. Поиск шёл через `rg` с явными каталогами `internal`, `cmd`, `docs`. База: `origin/main` `f25294ba`.

Исследованы `internal/domain/verdict.go`, `internal/domain/types.go`, `internal/domain/transition.go`, `internal/app/orchestrator/checkall.go` и `internal/app/orchestrator/verdict_contract_test.go`.

Не повторяются `BCX-18` (дубль `NormalizeConclusion`), `BCX-19` (написание machine-кодов), `BCX-20` (`green_blocked` в документации), `AUD-01` (auto-merge cibridge), `verdict-contract-gaps.md` (информационные проверки и `required:false`), `BCX-12` (семь наборов констант) и `T3.424` (`validVerdict`). Новые находки лежат в выборе движка вердикта, в семантике слота и в машине состояний.

## VC-01. Два типа `green_blocked` с противоположной семантикой слота

`internal/domain/types.go:83-86`, символ `VerdictGreenBlocked`. Комментарий говорит: «It frees no slot until the blocker closes».

`internal/domain/verdict.go:931-948`, символ `GreenBlockedVerdict`. Комментарий говорит, что задача «frees its execution slot». `internal/domain/verdict.go:951-956`, символ `ReleasesSlot`, возвращает `true` для `GreenBlocked`.

`internal/app/orchestrator/verdict_contract_test.go:30` закрепляет равенство строк обоих типов. Один и тот же literals `green_blocked` означает и удержание слота, и его освобождение.

Оба символа не имеют производственных вызывающих. Поиск по `internal` и `cmd` находит только объявления и тесты.

## VC-02. `EvaluateGreenBlocked` читает не тот движок вердикта

`internal/domain/verdict.go:972-973`, символ `EvaluateGreenBlocked`, вызывает `Evaluate`, а не `EvaluatePR`.

`internal/domain/verdict.go:116`, символ `Evaluate`, требует `ShipAllowed` в строке 161. `internal/domain/verdict.go:186`, символ `EvaluatePR`, не применяет формулу поставки и разрешает готовность doc-only PR в строках 271-279. Реальный цикл CI вызывает `EvaluatePR` в `internal/infra/cibridge/bridge.go:921`, символ `Refresh`.

Следствие: Watch, который `EvaluatePR` публикует как `ready`, не становится `GreenBlocked`, если `Snapshot.Ship` пуст. Слот оркестратора не освобождается. `EvaluateGreenBlocked` не имеет производственного вызывающего.

## VC-03. Машина состояний допускает переходы вне документированной таблицы

`internal/domain/transition.go:249-293` перечисляет разрешённые переходы. Среди них нет `blocked -> parked`, `blocked -> needs_input`, `parked -> blocked` и `needs_input -> blocked`.

`internal/domain/transition.go:365-379`, символ `parkOnFailure`, отклоняет только `StatusClosed` и `StatusParked`. Переход `blocked -> parked` проходит.

`internal/domain/transition.go:348-356`, символ `parkForInput`, отклоняет `StatusClosed`, `StatusNeedsInput` и `StatusParked`. Переход `blocked -> needs_input` проходит и оставляет поле `Blocked` в Watch без очистки.

`internal/domain/transition.go:388-397`, символ `blockWatch`, отклоняет `StatusClosed` и `StatusBlocked`. Переходы `parked -> blocked` и `needs_input -> blocked` проходят.

`internal/domain/transition.go:403-411`, символ `unblockWatch`, работает только из `StatusBlocked`. После `blocked -> needs_input` разблокировка становится недоступной.

## VC-04. Параллельная машина состояний в `SQLVerdictWriter.Publish`

`internal/app/orchestrator/checkall.go:812-918`, символ `SQLVerdictWriter.Publish`, пишет вердикт, статус `notifying` и `revision+1` напрямую в payload Watch в строках 863 и 869-874. Затем метод вставляет обязательства в строках 884-915.

Единственный guard находится в строке 842: текущий вердикт должен быть `watching`, а head должен совпадать.

`internal/domain/transition.go:483-516`, символ `transitionVerdict`, содержит канонический переход. `internal/domain/transition.go:523-529`, символ `validVerdict`, отвергает неизвестные вердикты. `Publish` не вызывает ни один из этих символов. Строка вердикта вне доменного набора попадает в Watch и в обязательства без проверки.

## VC-05. `checksPayload` помечает каждую проверку как обязательную

`internal/app/orchestrator/checkall.go:922-934`, символ `checksPayload`, пишет `"required": true` для каждой проверки в строке 932.

`internal/domain/types.go:154-159`, символ `Check`, объявляет поле `Required`. `internal/domain/verdict.go:221-229`, символ `EvaluatePR`, читает это поле, чтобы исключить информационную проверку из вердикта `blocked`.

Пока `checksPayload` пишет константу, любая проверка, сохранённая в Watch, считается обязательной при последующем чтении. Информационная проверка не может быть отфильтрована ниже по потоку. `verdict-contract-gaps.md` описывает ту же проблему на уровне провайдеров, но не описывает этот writer в слое app.

## VC-06. `FailedChecks` и `VerdictFromChecks` по-разному читают заключения

`internal/app/orchestrator/checkall.go:442-462`, символ `VerdictFromChecks`, нормализует заключение через `strings.ToUpper` и `strings.TrimSpace` в строке 448.

`internal/app/orchestrator/checkall.go:465-474`, символ `FailedChecks`, сравнивает сырое заключение с `"FAILURE"` и `"CANCELLED"` в строке 468 без нормализации.

Проверка с заключением `failure` в нижнем регистре даёт вердикт `retry`, но пустой список `FailedChecks`. Строка `Row` в `internal/app/orchestrator/checkall.go:320-326` уносит вердикт без имён проверок.

## Итог

Найдено шесть остатков контракта. Два из них лежат в домене вердиктов, один в выборе движка, три в машине состояний и в слое app. Пакет `research-verdict-contract-batch.json` вносит шесть карточек VC-01..VC-06, ADR 0306, eval `verdict-contract-state-machine` и восемь фактов с владельцами.
