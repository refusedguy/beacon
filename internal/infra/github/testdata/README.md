# T2.16. Фикстуры особых случаев GitHub

Записанные GraphQL- и REST-ответы для движка вердиктов. Контракт ответов
совпадает с T2.11: тело GraphQL имеет `data` с ключом алиаса `p0`, необязательным
массивом `errors` и `rateLimit.cost`. У каждой фикстуры `rateLimit.cost = 1`
(отчёт A: один батч с `statusCheckRollup` стоит 1 поинт).

## Состав

`cases.json` — манифест. Для каждой записи: `id`, `case`, `watch`, `policy`,
пути к записанным ответам и `expected` с вердиктом, причиной и способом
привязки. Пути относительные, от корня этого каталога.

Всего девять фикстур на восемь случаев: случай `behind` записан дважды, по
одному разу для `requireUpToDate=true` и `requireUpToDate=false`.

| id | случай | ожидаемый вердикт | причина | привязка |
|---|---|---|---|---|
| fork | fork | ready | — | headSHA |
| missing-required | missing-required | timeout | required checks not created: test,lint | number |
| skipped-neutral | skipped-neutral | ready | SKIPPED и NEUTRAL допустимы | number |
| pending-states | pending-states | watching | checks pending | number |
| draft | draft | watching | draft | number |
| behind-true | behind | rebase | behind and requireUpToDate | number |
| behind-false | behind | ready | — | number |
| merged | merged | merged | терминальный | number |
| closed | closed | closed | терминальный | number |

## Что проверяет каждая фикстура

- **fork.** REST workflow run несёт пустой `pull_requests`, поэтому проверку
  нельзя привязать по номеру PR. GraphQL tier-1 возвращает `headRefOid`, равный
  `watch.headSHA`. Привязка по headSHA даёт вердикт ready.
- **missing-required.** Все видимые проверки зелёные, но branch protection
  требует `build`, `test`, `lint`, а в tier-2 присутствует только `build`.
  Несозданные required checks ведут к noChecksTimeout, timeout и эскалации.
- **skipped-neutral.** В tier-2 есть CheckRun с `SKIPPED` и с `NEUTRAL`. Оба
  допустимы и не мешают вердикту ready.
- **pending-states.** CheckRun-ы со статусами `IN_PROGRESS`, `WAITING`,
  `QUEUED` и с conclusion `ACTION_REQUIRED` дают watching с причиной.
- **draft.** `isDraft=true` при зелёном rollup не даёт ready.
- **behind-true / behind-false.** Один и тот же `mergeStateStatus=BEHIND` при
  двух значениях политики: rebase и ready.
- **merged / closed.** Терминальные состояния.

## Тест

Тест живёт в родительском пакете: `internal/infra/github/fixtures_contract_test.go`.
Он самодостаточен, описывает структуры разбора локально и не импортирует
символы из `graphql.go`, `rest.go` или домена. Пути из `cases.json` считаются
относительными этого каталога, тест добавляет префикс `testdata/`.

Тест проверяет четыре вещи:

1. Presence: все восемь случаев из карточки присутствуют.
2. Parse: каждый записанный ответ разбирается в контракт T2.11 и несёт
   `rateLimit.cost >= 1`.
3. Verdict: ожидаемый вердикт из `cases.json` совпадает с локальной
   реализацией таблицы вердиктов WORKER-PROMPT §4.6.
4. Special: fork привязывается по headSHA, BEHIND проверен при обоих
   значениях политики.

Файл лежит в пакете `github`, поэтому `go test ./...` выполняет его в CI.
`TestMain` включает подробный вывод тестового бинарника. При падении теста
`go test` печатает накопленный вывод, и в логе CI видны строки
`=== RUN TestFixturesProduceExpectedVerdicts/fork` и `--- FAIL: ...`. При
зелёном прогоне `go test ./...` без `-v` отбрасывает вывод бинарника и печатает
только `ok <package>`. Это решение самого `go test`, из теста его не изменить.

Локальная таблица вердиктов это контрольный эталон для самих фикстур. Тест
движка вердиктов T1.5 может грузить те же файлы и сравнивать вердикт уже с
реальной функцией.
