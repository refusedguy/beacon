# Исследование: обработка ошибок и коды выхода

Дата: 2026-11-22. Проверено на `origin/main` `ad9f3889b285e20e643c9d1867862340f65045e4`.

## Как снимался замер

Исследование read-only. Команды `go test`, `go build`, `beacon gates` и `batch-apply` не запускались. Факты собраны из исходников в worktree `/tmp/opencode/research-eh-ses3de8`, который создан от `origin/main`. Поиск шёл через `rg` с явными каталогами `internal`, `cmd` и `docs`. Каждое утверждение ниже содержит путь, номер строки и символ.

Проверены зоны из задания: `internal/infra/httpapi/error*.go`, `internal/domain/verdict.go`, `cmd/beacon/*`, `internal/infra/mcp/tool_*.go`. Production-файла `error.go` в `httpapi` нет. В каталоге лежит только `error_format_test.go`, а форму ответа задаёт `writeError` в `internal/infra/httpapi/server.go:1075`.

## Что не вошло в бэклог

Эти разрывы уже закрыты или описаны в других карточках. Повторных карточек нет.

- `STARTUP_FAILURE` есть в обоих нормализаторах: `internal/domain/verdict.go:305` и `internal/app/orchestrator/checkall.go:718`.
- Расхождение `ACTION_REQUIRED` между нормализаторами уже описано в `docs/tasks/ATX-V07.md:11`. Карточка имеет статус todo.
- Вердикт `timeout` в `get_result` и причина `timeout` в `wait_result` разведены в `internal/infra/mcp/tool_get_result.go:419` (`nextStepFor`).
- Старого пакета `internal/infra/server/http` нет, два HTTP-формата ошибки больше не сосуществуют. Разница форматов зафиксирована в `docs/tasks/ARDH-21.md:19`.
- Код выхода валидации 1 против 2 описан в `docs/tasks/BCX-26.md:11`.
- Код `stop_uncertain` и его exit code 2 описаны в `docs/tasks/T5.52.md:20` и `docs/tasks/BCX-13.md:11`.

## Таблица находок

| ID | Фаза | Разрыв | Файл:строка | Символ |
| --- | --- | --- | --- | --- |
| EH-01 | P1 | Ошибка чтения стора превращается в `WATCH_NOT_FOUND` (404) | `internal/infra/mcp/tool_get_result.go:998-1003` | `mapResultReadError` |
| EH-03 | P1 | Код `INVALID_INPUT` уходит со статусом 415, реестр предписывает 400 | `internal/infra/httpapi/server.go:1027`, `internal/domain/types.go:683` | `writeError`, `statusByErrorCode` |
| EH-02 | P2 | Сбой получения failure excerpt молчалив для агента | `internal/infra/mcp/tool_get_result.go:526-529`, `internal/infra/mcp/schema.go:77` | `FailureExcerpt` |
| EH-04 | P2 | `ghError` отбрасывает все строки stderr после первой | `cmd/beacon/orchestrate.go:59-76` | `firstLine`, `ghError` |

## P0

Нет подтверждённых разрывов уровня P0. Каждый кандидат из заданной зоны либо закрыт в `origin/main`, либо уже имеет карточку. Раздел оставлен пустым намеренно.

## P1

### EH-01. Ошибка чтения стора превращается в код «не найдено»

`internal/infra/mcp/tool_get_result.go:998` определяет `mapResultReadError`. Функция сохраняет `domain.AppError` через `errors.As` на строке 1000. Любая другая ошибка попадает в `domain.NewAppError(domain.CodeWatchNotFound, what+": "+err.Error())` на строке 1003. Комментарий на строках 994-997 прямо говорит, что узкий порт не различает отсутствующую строку и сбой ввода вывода.

Вызовы функции: `tool_get_result.go:446` (`get_result: watch`), `:455` (`get_result: obligation`), `:1261` (`grade_result: watch`), `:1282` (`grade_external_action: watch`).

Реестр задаёт `CodeWatchNotFound` статус 404 в `internal/domain/types.go:684`. Там же определён `CodePersistFailed` со статусом 503 в `internal/domain/types.go:691`. В этом пути он не используется. Агент получает код «Watch не найден» и прекращает наблюдение, хотя причинной была ошибка диска или SQL. Повторная попытка для такой ошибки уместна, но контракт её не предлагает.

Отдельно в той же функции на строке 255 стоит честный `not found` для обязательства. Разница между `:255` и `:1003` показывает, что автор различал два случая, но обобщил их в одном helper.

### EH-03. Один код ошибки имеет два статуса

`internal/infra/httpapi/server.go:1027` вызывает `writeError(w, http.StatusUnsupportedMediaType, string(domain.CodeInvalidInput), ...)`. Статус 415 уходит вместе с кодом `INVALID_INPUT`. Остальные вызовы того же кода в этом файле передают 400: строки 1301, 1308 и 1312.

Каноническая таблица `internal/domain/types.go:683` задаёт `CodeInvalidInput: 400`. Функция `internal/domain/types.go:698` (`StatusForCode`) читает эту таблицу. Конструктор `NewAppError` в `internal/domain/types.go:715` берёт статус из реестра и не даёт коду уйти с другим статусом. HTTP-граница обходит этот механизм, потому что `writeError` в `internal/infra/httpapi/server.go:1075` принимает статус отдельным аргументом и не сверяет его с кодом.

Клиент, который ветвится на `code` из тела ответа, видит один и тот же `INVALID_INPUT` для двух разных HTTP-статусов. Контракт кода перестаёт быть однозначным.

## P2

### EH-02. Сбой получения excerpt не виден агенту

`internal/infra/mcp/tool_get_result.go:526` вызывает источник failure excerpt. На строке 528 ошибка пишется только в журнал через `observe.ActorError`. На строке 529 `raw` заменяется пустой строкой. На строке 536 пустое значение попадает в `FailedCheck.FailureExcerpt`.

Поле объявлено в `internal/infra/mcp/schema.go:77` как `FailureExcerpt string` с тегом `json:"failureExcerpt,omitempty"`. Пустая строка исчезает из ответа. Агент не может отличить три ситуации: у проверки нет аннотаций, источник не настроен, запрос к GitHub упал. Комментарий в `schema.go:74-76` описывает только первый случай.

Источник усиливает эффект. `envFailureExcerptSource.FailureExcerpt` в `tool_get_result.go:764` возвращает `"", nil` при пустом `repo`, `headSHA` или `checkName`. На строке 767 он же возвращает `"", nil` при некорректном формате `repo`. Оба возврата не считаются ошибкой и не попадают даже в журнал.

В ответе есть индикатор усечения `FailureExcerptTruncated` в `internal/infra/mcp/schema.go:156`. Индикатора «не удалось получить текст ошибки» нет.

### EH-04. Multiline-диагностика gh теряется

`cmd/beacon/orchestrate.go:59` определяет `firstLine`. Функция обрезает строку на первом переводе строки или возврате каретки. `ghError` на строке 68 вызывает её для stderr на строке 69 и для stdout на строке 71. Итоговая ошибка собирается на строке 76 через `fmt.Errorf("%s: %s", what, reason)`.

Функция используется в двух путях. `cmd/beacon/orchestrate.go:94` (`cliRunner.Run`) передаёт ошибку адаптеру слияния GitHub. `cmd/beacon/daemon.go:786` (`daemonGHRunner`) передаёт её проверкам цикла. В обоих случаях вторая и последующие строки stderr исчезают. Для демона это выбрано намеренно, чтобы не писать подсказки CLI в журнал каждые три минуты. Для пути слияния та же политика отбрасывает детали GraphQL или причину отказа, которые `gh` печатает после первой строки.

Соседние пути ведут себя иначе. `cmd/beacon/checkall.go:69` сохраняет весь обрезанный по пробелам stderr в формате `gh %s: %s`. `cmd/beacon/main.go:439` (`emit`) пересылает stderr целиком. В одном пакете существуют три политики по отношению к одному и тому же потоку.

## Что сделать по приоритету

1. Ввести отдельный код для неопределённой ошибки чтения или явно различать отсутствующую запись и сбой стора на порту `resultStoreReader`. Это вернёт `PERSIST_FAILED` вместо `WATCH_NOT_FOUND` в EH-01.
2. Сверить статус и код в `writeError` с `domain.StatusForCode`, когда код зарегистрирован. Для `INVALID_INPUT` со статусом 415 либо добавить отдельный код, либо передавать 400.
3. Добавить в ответ `get_result` признак того, что получение excerpt не удалось, и убрать молчаливые возвраты в `envFailureExcerptSource.FailureExcerpt`.
4. Разделить политики логирования демона и диагностики слияния, чтобы `ghError` сохранял все строки stderr для адаптера GitHub.

## Счётчики

Карточек: 4. P0: 0. P1: 2. P2: 2. Eval-кейсов: 4. Зафиксировано фактов: 6.
