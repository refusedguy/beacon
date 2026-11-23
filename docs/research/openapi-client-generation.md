# Генерация Go-клиента OpenCode из OpenAPI: исследование

Дата: 2026-11-21. Репозиторий: `beacon-impl`. Режим только чтение: код, конфиги и тесты не менялись, `go test`, `go build`, `beacon gates` и инструменты `tools.beacon.*` не запускались. Локальный поиск шёл через `rg` с явными каталогами `docs/api/`, `docs/adrs/`, `docs/research/`, `internal/infra/opencode/`, `internal/infra/httpapi/`, `internal/tui/`, `cmd/beacon/`, `.github/`, `tools/`. Внешние документы читались через `webfetch` и `context7`: спецификация OpenCode и документация oapi-codegen, openapi-generator.

## 1. Короткий ответ

Предмет задачи не совпадает с содержимым репозитория. Файл `docs/api/openapi.yaml` описывает собственный loopback API Beacon, а не API OpenCode. В нём четыре операции, и ни одной из перечисленных в задаче (`session.list`, `session.create`, `session.prompt`, `session.interrupt`, `agent.list`, `config`, `event`, `form`, experimental wait и log). Спецификация OpenCode живёт вне репозитория, по адресу `https://opencode.ai/docs/openapi.json`, и помечена как experimental.

Поэтому сгенерировать клиент OpenCode из `docs/api/openapi.yaml` нельзя в принципе. Рекомендация: оставить ручной клиент в `internal/infra/opencode/client.go` (вариант A). Если позже понадобятся типизированные DTO для форм, wait и log, использовать вариант C с зафиксированной локальной копией внешней спеки и генерацией только моделей. Полную генерацию (вариант B) не брать.

## 2. Что на самом деле лежит в `docs/api/openapi.yaml`

| Свойство | Значение |
| --- | --- |
| Спецификация | OpenAPI 3.1.0 |
| `info.title` | `Beacon loopback projection API` |
| `info.version` | `1.0.0` |
| Размер | 791 строка, 23 283 байта |
| Путей и операций | 4 |
| Схем в `components/schemas` | 20 |
| Переиспользуемых ответов | 4 (`Unauthorized`, `BadInput`, `Forbidden`, `UnsupportedMediaType`) |
| Схем безопасности | 1 (`bearerAuth`, HTTP bearer) |
| `$ref` | 33, все локальные вида `#/components/schemas/...` и `#/components/responses/...` |

Операции файла:

| operationId | Метод и путь | Строка |
| --- | --- | --- |
| `getHealth` | GET `/api/v1/health` | 31 |
| `getSnapshot` | GET `/api/v1/snapshot` | 74 |
| `streamEvents` | GET `/api/v1/events` (SSE) | 150 |
| `decide` | POST `/api/v1/human/decide` | 207 |

Внешних `$ref` нет, циклических ссылок при чтении схем не найдено. Для генератора это чистый файл: он не сломается на ссылках. Но он описывает не тот сервис. Сейчас этот же контракт обслуживают два ручных клиента: `internal/infra/httpapi/client.go` и `internal/tui/client.go`. Тест `internal/infra/httpapi/contract_test.go:29,217,260` грузит этот файл через `loadOpenAPI` и проверяет ответы и примеры. Генерация отсюда затронула бы именно этот контракт и не дала бы ни одного OpenCode-метода.

Отдельно зафиксирую: в задаче сказано «сгенерировать единого Go-клиента OpenCode из docs/api/openapi.yaml». Это утверждение проверено и отвергнуто. Имена из задачи находятся в другом документе, и он не хранится в репозитории.

## 3. Спецификация OpenCode, которая реально нужна Бикону

Источник: `https://opencode.ai/docs/openapi.json`, прочитан 2026-11-21. Ранее тот же источник разбирался в `docs/research/exp-api-surface.md:5-9`.

| Свойство | Значение |
| --- | --- |
| Спецификация | OpenAPI 3.1.0 |
| `info.title` | `opencode HttpApi` |
| `info.version` | `0.0.1` |
| Описание | `Experimental HttpApi surface for selected instance routes.` |
| Размер | 31 467 строк JSON |
| Операций | 173 |
| Схем | 518 |
| `components.securitySchemes` | пустой объект, операции объявляют `security: []` |
| `text/event-stream` | 2 ответа |

Нужные Бикону operationId в этой спецификации имеют следующее точное написание:

| operationId | Метод и путь | Статус в адаптере |
| --- | --- | --- |
| `session.list` | GET `/api/session` | Вызывается, `internal/infra/opencode/client.go:315` |
| `session.create` | POST `/api/session` | Вызывается, `:345` |
| `session.prompt` | POST `/api/session/{sessionID}/prompt` | Вызывается, `:373` |
| `session.interrupt` | POST `/api/session/{sessionID}/interrupt` | Вызывается через `opencode api`, `internal/infra/wake/deliver.go:888` |
| `agent.list` | GET `/api/agent` | Вызывается, `:301` |
| `config.get` | GET `/api/config` | Вызывается, `:388`. В задаче имя `config` |
| `event.subscribe` | GET `/api/event` | Вызывается, `:418` |
| `form.list` и `session.form.*` | GET/POST/DELETE `/api/form`, `/api/session/{id}/form` | Не вызываются |
| `experimental.session.wait` | POST `/api/experimental/session/{id}/wait` | Не вызывается, только упоминания в ADR |
| `experimental.session.log` | GET `/api/experimental/session/{id}/log`, SSE | Не вызывается |

Признаки спецификации, которые важны для генератора:

1. Имена operationId и схем содержат точки. Примеры: `session.list`, `agent.list`, `Agent.Info`, `Session.ID`, `InvalidRequestErrorEncoded`. Из 518 схем 440 содержат точку. Генераторы превращают их в Go-имена по CamelCase, поэтому имя в коде не совпадает с именем в спецификации. Замер: после удаления точек и приведения к нижнему регистру совпадений имён нет, то есть коллизий на этом уровне не возникло.
2. Много `anyOf` с `$ref` и `null`, а также `anyOf` с несколькими `$ref`. oapi-codegen строит для них union-типы с методами `As*`, `From*` и `Merge*` и импортирует `github.com/oapi-codegen/runtime`.
3. Авторизация в спецификации не описана. Операции несут `security: []`, Basic-авторизация `opencode:<password>` берётся из `$XDG_STATE_HOME/opencode/service.json`. Сгенерированный клиент сам этот файл не прочитает, нужен ручной слой.
4. SSE и параметры `deepObject` требуют ручной настройки транспорта. Клиент в `internal/infra/opencode/client.go` хранит TTL-кэш, переподключение без нагона истории, ограничение размера кадра и тела ответа. Генератор такого не создаёт.
5. Пути с префиксом `/api/experimental/` могут меняться без гарантии совместимости. Это прямо сказано в описании документа.

## 4. Доступные генераторы и их требования

| Генератор | Версия | Что даёт | Во внешней зависимости | Ограничение |
| --- | --- | --- | --- | --- |
| oapi-codegen | v2.8.1 | OpenAPI 3.1, config с `generate: {models, client}`, client и/или только модели | Импорт `github.com/oapi-codegen/runtime` в сгенерированном коде; модуль требует Go 1.24+ | Union-типы для `anyOf`, переименование точечных имён по CamelCase, встроенное разрешение коллизий |
| openapi-generator | 7.x | Generator `go`, stability STABLE, тип client | Сгенерированный код на stdlib, отдельного Go-рантайма нет | Для запуска нужен Java или Docker; без фильтрации создаёт SDK на все 173 операции и 518 моделей |

Генератор в репозитории не используется. Проверено: `rg` по `cmd`, `internal`, `.github`, `*.mod`, `*.yaml` не нашёл `go:generate`, `oapi-codegen`, `openapi-generator` и маркеров `Code generated` в Go-файлах.

Влияние на ADR:

- ADR 0016 (`docs/adrs/0016-build-system.md:7`) требует только стандартную библиотеку от бинарника `beacon-gates`. У этого бинарника отдельный `tools/beacon-gates/go.mod` из трёх строк без `require`. Добавление `github.com/oapi-codegen/runtime` в корневой `go.mod` не меняет бинарник гейтов.
- ADR 0023 (`docs/adrs/0023-gates-binary-toolchain.md:9-10`) фиксирует флаги сборки и побайтовый хеш гейтов. Корневые зависимости на этот хеш не влияют. Ключ кэша CI включает `go.sum`, поэтому первый эффект это инвалидация кэша, а не красная проверка.
- ADR 0060 (`docs/adrs/0060-cli-adapter-boundary-and-live-loop.md:29`) прямо говорит: HTTP-клиент строится на stdlib `net/http` без сторонних зависимостей. Импорт `oapi-codegen/runtime` в этот клиент требует правки ADR 0060 или отказа от union-типов через overlay.
- ADR 0060 в разделе «Отклонённое» (`:65`) отвергает широкий HTTP-клиент. Генерация всех 173 операций идёт вразрез с этим решением. ADR 0006 и 0024 запрещают переносить код OpenCode в ядро, поэтому сгенерированный пакет остаётся в `internal/infra/opencode` и не должен попадать в `internal/domain` или `internal/app`.

## 5. Сравнение трёх вариантов

| Вариант | Effort | Риск | Поддержка при изменении спеки | Golden contract | CI |
| --- | --- | --- | --- | --- | --- |
| A. Один ручной клиент | Низкий. Клиент уже есть, около 546 строк в `internal/infra/opencode/client.go`, покрыт `client_test.go` | Низкий. Известное поведение, тесты на fakeDoer без сети | Ручная правка под каждую новую ручку. Сейчас не вызываются формы, wait и log | Не трогается. MCP golden (9 инструментов, 18 212 байт, SHA `ba0a9f...` в `internal/infra/mcp/contract_test.go:31-36`) и HTTP golden из `contract_test.go` остаются прежними | Без изменений |
| B. Полная генерация от OpenAPI | Высокий. Нужны локальная копия внешней спеки, выбор генератора, слой под `service.json` и Basic-авторизацию, вручную настроенную SSE-подписку | Высокий. Спека experimental с версией 0.0.1; конфликт с ADR 0060 п6 и отклонённым широким клиентом; новая зависимость в `go.mod` | Каждое обновление upstream меняет 518 моделей и 173 операции, diff большой и шумный | Напрямую не ломает MCP golden, но создаёт второй источник типов, который может разойтись с ручными DTO и с ответами `contract_test.go` | Нужен шаг генерации и проверка расхождения спеки, без них сгенерированный код застареет |
| C. Генерация DTO, ручной транспорт | Средний и высокий. Нужны overlay, конфиг генератора, адаптация DTO и сохранение текущего транспорта | Средний. Зависимость от схем upstream остаётся, но транспорт и кэш не меняются | Модели регенерируются, транспорт живёт отдельно и правится редко | Низкий при сохранении JSON-формы полей. сгенерированный код не входит ни в MCP golden, ни в HTTP golden | Добавляется шаг «сгенерировать и сравнить с закоммиченным результатом» |

Отдельно про риск B: даже если сгенерировать клиент, он не заменяет ручной слой целиком. Basic-авторизация из `service.json`, TTL-кэш `session.list`, переподключение SSE без нагона истории и ограничение размера ответа остаются ручным кодом. Получается не один сгенерированный клиент, а сгенерированный клиент плюс обёртка, то есть больше кода, чем сейчас.

## 6. Рекомендация

Оставить вариант A. Причины по порядку:

1. Исходная посылка неверна: `docs/api/openapi.yaml` не содержит API OpenCode.
2. Внешняя спецификация experimental, версия 0.0.1, и не обещает совместимости. Внутри неё 173 операции и 518 схем, из которых Бикону нужно около пятнадцати.
3. Текущий клиент уже реализует поведение, которого генератор не даёт: чтение `service.json`, Basic-авторизацию, кэш с TTL, SSE с переподключением, ограничение размеров, совместимость поля `model` со строкой и объектом (`internal/infra/opencode/client.go:147-177`).
4. ADR 0060 п6 и раздел «Отклонённое» запрещают широкий клиент и сторонние зависимости в этом слое.
5. Вариант A не меняет golden contract и не добавляет шагов в CI.

Дешёвое дополнение к A: хранить SHA-256 внешней спеки отдельным файлом и проверять его в CI раз в интервал. Это покажет дрейф upstream без генерации кода и без новых зависимостей.

Если владелец решит, что типизированные DTO для форм, wait и log нужны, брать вариант C. Условие: генерировать только модели по урезанной копии спеки и не трогать транспорт. Ниже точные шаги для этого случая.

## 7. Шаги при генерации (вариант C)

1. Зафиксировать внешнюю спецификацию локально, отдельным файлом, чтобы не смешивать её с контрактом Beacon:

   ```sh
   curl -fsSL https://opencode.ai/docs/openapi.json -o docs/api/opencode.openapi.json
   sha256sum docs/api/opencode.openapi.json | tee docs/api/opencode.openapi.json.sha256
   ```

   Файл `docs/api/openapi.yaml` не трогать. Он остаётся источником правды для `internal/infra/httpapi/contract_test.go`.

2. Создать overlay `docs/api/opencode-client.overlay.yaml`, который оставляет только нужные пути: `/api/session`, `/api/session/{sessionID}`, `/api/session/{sessionID}/prompt`, `/api/session/{sessionID}/interrupt`, `/api/agent`, `/api/config`, `/api/event`, `/api/form`, пути форм, `/api/experimental/session/{sessionID}/wait` и `/api/experimental/session/{sessionID}/log`. Неиспользуемые схемы oapi-codegen отсечёт сам при стандартном `skip-prune: false`. В тех же действиях overlay упростить `anyOf` с `null` до обычного nullable-поля, если нужна генерация без `github.com/oapi-codegen/runtime` и без правки ADR 0060.

3. Добавить конфиг `tools/oapi-codegen/config.yaml`:

   ```yaml
   package: openapiclient
   output: internal/infra/opencode/gen/models.gen.go
   generate:
     models: true
   output-options:
     overlay:
       path: docs/api/opencode-client.overlay.yaml
       strict: true
   ```

4. Запускать генерацию из корня репозитория, все пути в конфиге относительны корня:

   ```sh
   go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.1 \
     -config tools/oapi-codegen/config.yaml \
     docs/api/opencode.openapi.json
   ```

   Версия генератора зафиксирована в команде, поэтому в `go.mod` он не попадает. Если overlay не убрал `anyOf`, сгенерированные файлы импортируют `github.com/oapi-codegen/runtime`, и тогда `go mod tidy` добавит модуль в корневой `go.mod`. Это требует правки ADR 0060 п6. Альтернатива: вместо oapi-codegen взять генератор `go` из openapi-generator, его вывод остаётся на stdlib, но сам запуск требует Java или Docker.

5. Оставить `internal/infra/opencode/client.go` владельцем транспорта. Заменить локальные анонимные struct в DTO на функции преобразования из сгенерированных моделей на границе `decodeList`, `decodeSingle` и разбора события. TTL-кэш, `Doer`, Basic-авторизация, SSE, максимальные размеры тела и кадра и `modelID.UnmarshalJSON` остаются ручным кодом.

6. Добавить в `.github/workflows/ci.yml` шаг проверки расхождения:

   ```sh
   go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.1 \
     -config tools/oapi-codegen/config.yaml \
     docs/api/opencode.openapi.json
   git diff --exit-code -- internal/infra/opencode/gen docs/api/opencode.openapi.json
   sha256sum -c docs/api/opencode.openapi.json.sha256
   ```

   Шаг идёт в задаче `test` корневого модуля. Модуль `tools/beacon-gates` и его хеш из ADR 0023 не затрагиваются.

7. Порядок обновления при смене upstream: скачать новую копию, пересчитать SHA, посмотреть diff результата после overlay, поправить overlay если ручки исчезли или изменились, прогнать генерацию, закоммитить спеку, SHA и сгенерированный файл в одном коммите. Ручная правка `models.gen.go` недопустима, её ловит шаг `git diff --exit-code`.

## 8. Что не проверялось

Живой сервер OpenCode не поднимался, реальные запросы не делались. Спецификация ни в одном генераторе не прогонялась: задача была read-only, поэтому утверждения о выводе генераторов взяты из их документации, а не из запуска. Тесты, сборка и гейты не запускались. Числа по локальному файлу и по внешней спецификации получены чтением исходников и подсчётом через `rg`.
