# Инфраструктура тестов и gate-скриптов: остаточные разрывы

Дата: 2026-11-22. Снято с `origin/main` на коммите `ad9f3889`. Режим: чтение. `go test`, `go build`, гейты и `batch-apply` локально не запускались. Источники: `cat -n`, `rg` и `git log/blame` по `tools/beacon-gates/`, `.github/workflows/`, `internal/testsystem/`, `tests/adapter/`, `cmd/batch-apply/` и `internal/infra/batchapply/`.

## Границы

Не дублирую `research-e2e-coverage`, `ci-gates-gaps.md` (C01-C08), `ci-speed-audit.md`, `flaky-tests*.md` и карточки BCX-51/54/55/56. Вопросы обязательных чеков, кэша setup-go, неподключённого reachability-гейта, поля `owner_field`, лимитов exception-таблиц, живого e2e и seeded-artery здесь не повторяются. Production-код не менялся. Все owner_files в карточках существуют и не пересекаются внутри одной фазы.

## Таблица находок

| ID | Приоритет | Разрыв | file:line и символ | Решение | Эффект |
| --- | --- | --- | --- | --- | --- |
| TI-01 | P0 | Проверка `owner_files` пакета живёт только в тесте | `internal/infra/batchapply/apply_test.go:614` `missingOwnerFileFormat`, `:615` `overlapOwnerFileFormat`, `:622` `batchPackProblems`; `internal/infra/batchapply/apply.go:919` `Run`, `:950` `cardFieldProblems`, `:959` `cardIDProblems`, `:625` `fillCardDefaults` | Перенести `batchPackProblems` в production и вызывать его из `Run` до `-check`, `-dry-run` и `-commit` | `-check` и `-commit` перестанут пропускать несуществующие файлы и пересечения owner_files в одной фазе |
| TI-02 | P1 | У exception-таблиц нет позитивного теста подавления и `CAP-PERMANENT-STALE` | `tools/beacon-gates/gate/exceptions.go:97` `covered`, `:115`, `:139-148` `CAP-PERMANENT-STALE`; `tools/beacon-gates/gate/gate_test.go:117` `TestExceptionsApply`; `tools/beacon-gates/capabilities/selftest.go:51-52`, `tools/beacon-gates/reachability/selftest.go:76` | Добавить фикстуру с реальной строкой `permanent.json` и проверкой, что находка гасится; отдельный тест на `CAP-PERMANENT-STALE` | Регрессия в `Apply` не сможет молча спрятать находку или завалить CI ложными строками |
| TI-03 | P2 | У джобы `js-test` нет таймаута | `.github/workflows/ci.yml:183` `js-test`, `:186` `steps:`; остальные джобы имеют `timeout-minutes: 10` в `:31, :53, :95, :136, :160, :221, :288, :318, :364` | Добавить `timeout-minutes: 10` в джобу `js-test` | Зависший `node --test` не будет удерживать PR 6 часов по умолчанию GitHub |
| TI-04 | P2 | Канонический текст trace не экранирует разделители полей | `internal/testsystem/harness.go:290` `encodeFields`, `:110` `Event.String`, `:116` `Result.String`, `:128` `Fingerprint`, `:165` `Regression.Check`; `internal/testsystem/harness_test.go:46` `TestGoldenFingerprint` | Кодировать ключ и значение перед склейкой или hashing по структуре, а не по тексту; добавить тест на коллизию | Два разных набора полей не смогут дать один fingerprint и пройти `Check` |

## P0

### TI-01. Валидация owner_files batch-пакета живёт только в тестовом файле

`internal/infra/batchapply/apply_test.go:622` определяет `batchPackProblems`. Функция проверяет три вещи: существование файла (`:635`, `:640`), отсутствие каталога вместо файла и выход за пределы дерева (`:635`), а также пересечение `owner_files` внутри одной фазы (`:645`). Форматы сообщений объявлены в `:614-615` и не встречаются больше ни в одном production-файле (проверено `grep` по `internal/infra/batchapply/*.go`).

Production-путь `internal/infra/batchapply/apply.go:919` `Run` читает пакеты через `loadBatches` (`:936`), затем вызывает только `declaredFactProblems` (`:946`), `cardFieldProblems` (`:950`) и `cardIDProblems` (`:959`). Вызова `batchPackProblems` нет. Значит `-check` (`:977`), `-dry-run` и `-commit` принимают пакет с несуществующим `owner_file` и с двумя владельцами одного файла в фазе P0.

Дополнительно `internal/infra/batchapply/apply.go:625` `fillCardDefaults` подставляет owner files по подстроке в заголовке карточки (`acp`, `e2e`, `eval`, `plugin`, `bridge`, `verdict`, `ci`, иначе `deliver.go`). Подставленные пути тоже не проходят никакой проверки существования. Для карточки с заголовком без ключевых слов создадутся `internal/infra/wake/deliver.go` и `deliver_test.go` независимо от того, существуют ли они в дереве и свободны ли в этой фазе.

Комментарий в `.github/workflows/ci.yml:80-82` утверждает обратное: «несуществующий owner_file или два владельца одного файла в фазе проваливают обязательную проверку test». Шаг `:83-87` гонит `go test ./internal/infra/batchapply -run '^TestBatchPack'`. Тесты `:659` `TestBatchPackGateRejectsInvalidOwnerFiles` и `:761` `TestBatchPackGateReadsEveryJSON` действительно проверяют `batchPackProblems`, но на синтетических пакетах, созданных внутри теста. Они доказывают работу хелпера, а не то, что инструмент применяет это правило к реальным JSON.

## P1

### TI-02. Self-test гейтов не проверяет подавление находок и устаревшие permanent-строки

`tools/beacon-gates/gate/exceptions.go:84` `Exceptions.Apply` делает три вещи: гасит находки по ключу `rule\x00path` (`:97-120`), считает ratchet baseline (`:121-128`), помечает неиспользованные строки `CAP-BASELINE-STALE` (`:129-138`) и `CAP-PERMANENT-STALE` (`:139-148`).

`tools/beacon-gates/gate/gate_test.go:117` `TestExceptionsApply` проверяет только пустые таблицы (`:123`) и ratchet вместе с baseline-stale на искусственной записи (`:126-129`). Ни в одном тесте нет находки, совпадающей со строкой baseline или permanent, поэтому ветка `covered` на `:115` и генерация `CAP-PERMANENT-STALE` на `:142` не имеют позитивного покрытия.

Self-test'ы усиливают слепоту. `tools/beacon-gates/capabilities/selftest.go:51-52` передаёт в `analyse` пустой `gate.Exceptions{}`. `tools/beacon-gates/reachability/selftest.go:76` вызывает `analyse` напрямую, без `ex.Apply`. При этом `tools/beacon-gates/gate/config/permanent.json` содержит 19 строк, каждая из которых гасит реальную находку. Ошибка в `entryKey`, `activeUnder` или в цикле `:104-108` не будет поймана ни self-test'ом, ни `TestExceptionsApply`.

## P2

### TI-03. Джоба `js-test` работает без таймаута

`.github/workflows/ci.yml:183` объявляет джобу `js-test`; после `runs-on` в `:185` сразу идёт `steps:` в `:186`, без `timeout-minutes`. Во всех остальных джобах этого workflow таймаут явно задан: `:31`, `:53`, `:95`, `:136`, `:160`, `:221`, `:288`, `:318`, `:364`, везде 10 минут.

Шаг `:190-216` собирает `tests/adapter/*.test.js`, `.opencode/skills/**/SKILL_test.js` и `.opencode/agents/**/*_test.js`, затем выполняет один `node --test` без таймаута на level теста. Среди собранных файлов `tests/adapter/s2-channel.test.js` использует `watch` и polling (`:14-22`, `:381-384`). Зависший watcher или ожидание в любом из этих файлов оставит джобу до штатного лимита GitHub, что на PR выглядит как «зелёная, но вечная» проверка.

### TI-04. Ключи и значения полей склеиваются без экранирования

`internal/testsystem/harness.go:290` `encodeFields` сортирует ключи и пишет их как `key=value`, соединяя запятой (`:298-303`). Разделители `,` и `=` не экранируются. `Event.String` (`:110`) вставляет результат в `seq|kind|receipt|fields`, `Result.String` (`:116`) собирает строки, `Fingerprint` (`:128`) хэширует этот текст.

Два разных набора полей дают одинаковый канон: `{"a":"b,c=d"}` превращается в `a=b,c=d`, и `{"a":"b","c":"d"}` тоже превращается в `a=b,c=d`. Оба trace получают один fingerprint. `Regression.Check` (`:165-174`) сравнивает только fingerprint, поэтому изменение сценария с коллизией пройдёт проверку. `internal/testsystem/harness_test.go:46` `TestGoldenFingerprint` фиксирует один текст и один хэш, но не тестирует столкновения. Маркеры fault приходят из `tests/scenarios/seeded.yaml` через `SeedSpec.Marker` (`harness.go:336`) и попадают в поле `marker` в `:455-460`, то есть произвольные символы в языке сценариев допустимы.

## Что проверено и исключено

- `internal/infra/batchapply/apply.go:269` `cardFieldProblems` и `:304` `cardIDProblems` действительно вызываются из `Run`. Тип, фаза, статус и небезопасные ID отклоняются. Разрыв только в `owner_files`.
- `tools/beacon-gates/main_test.go:58` `TestRunSelfTests` прогоняет `--self-test` для всех шести зарегистрированных команд, поэтому self-test reachability в `go test ./...` присутствует. Проблема TI-02 в другом: self-test'ы не заходят в путь исключений.
- `tools/beacon-gates/gate/tool_contract.go:261` `DefaultToolsListSnapshot` указывает на `tools/beacon-gates/gate/testdata/tools-list.json`, которого в дереве нет (в `testdata` лежит только `seeds.txt`). Этот разрыв лежит в зоне BCX-51, поэтому в карточки не включён.
- `gh` и конфиг branch protection не проверялись, чтобы не дублировать C01 из `ci-gates-gaps.md`.
- e2e-сценарии, плагин `beacon-bridge.js`, seeded-artery и флейковость тестов не рассматривались: это зоны других агентов и карточек BCX-54/55/56.

## Ограничения проверки

Запуски `go test`, `go build`, гейтов и `batch-apply` запрещены правилами репозитория, поэтому все выводы получены чтением кода и конфигов. Коллизия в TI-04 выведена из формата `encodeFields`, а не из запущенного теста. Отсутствие вызова `batchPackProblems` в `Run` проверено `grep` по всем `.go` в `internal/infra/batchapply`. Актуальность строк `permanent.json` и `reachability.json` прогоном гейта не проверялась.
