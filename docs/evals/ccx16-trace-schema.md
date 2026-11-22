# CCx-16: версия схемы trace и связность writer, reader, fixture

## Baseline

До изменения в `internal/infra/store/atomic_write.go` была одна жёстко заданная версия (`defaultWakeSchemaVersion = 2`) и ноль проверок связности writer, decoder и fixture. Формат journal row и версия 2 сохраняются.

## Сценарий

Writer `CommitVerdictTx` пишет строку `wake-intent` в `events`. Reader `DecodeEvent` выбирает decoder по `schema_version`. Golden fixture в `schema_contract_test.go` хранит точные байты payload для версии 2.

## Таблица решений

| Изменение | Версия | Golden fixture | Результат |
| --- | --- | --- | --- |
| Поле payload, которое читает consumer | +1, новая запись в `wakeSchemaByReaderFields` | Новая fixture для версии | Тест проходит |
| Поле payload без новой записи в леджере | Без изменений | Без изменений | Тест падает: версия не выводится |
| Raw-поле, которое не читает consumer | Без изменений, остаётся 2 | Обновляется при изменении writer | Тест проходит, decoder возвращает то же событие |
| Изменение формата journal row | Требует отдельной карточки и миграции | Требует новой fixture | Запрещено приёмкой карточки |

## Проверки

- `TestWakeIntentWriterDecoderAndFixtureAgree`: version и payload прочитанной строки совпадают с версией 2 и golden fixture; decoder даёт одинаковый результат для строки и fixture.
- `TestWakeVersionNeedsLedgerEntryForReaderField`: reader-видимое поле без записи в леджере не получает версию.
- `TestRawOnlyPayloadChangeKeepsVersion`: raw-поле не поднимает версию и не меняет результат decoder.

## Негативный контроль

Добавить поле в payload writer и оставить версию 2. Тест `_Agree` падает на сравнении payload с fixture. После bumps версии и замены fixture тест проходит.
