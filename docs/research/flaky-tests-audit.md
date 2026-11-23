# Аудит флакности тестов beacon

Дата: 21 ноября 2026. Источник: чтение файлов и `rg` по явным каталогам `internal`, `cmd`, `tests`, `evals`, `tools`. Тесты, сборка и gates не запускались. Код не менялся.

## Итог

Пять главных находок:

1. Два известных инцидента подтвердились кодом. В `beacon-bridge.js:959` тест ждёт только `existsSync`, затем вызывает `JSON.parse` по недописанной строке. В `cmd/beacon/main_test.go:748` тест проверяет `nil` у глобальных портов, а хелпер `t3145RestorePorts` не сохраняет `installedServerEnsurer`. Результат зависит от порядка тестов в пакете.
2. В репозитории нет `t.Parallel()` и нет `math/rand`. Главный источник флакности не параллелизм тестов и не генератор случайных чисел, а реальное время и планировщик. Почти все риски сводятся к `time.Sleep`, `time.After`, опросу файлов и непредсказуемому моменту запуска горутины.
3. Худший кластер это `tests/e2e/e2e_test.go`. Там одновременно настоящий `opencode serve`, `os.MkdirTemp` без очистки, `freePort` с закрытием слушателя перед повторным bind, опрос лога регуляркой каждые 200 мс и ID из `time.Now().UnixNano()`. Один файл содержит пять независимых источников нестабильности.
4. Много тестов уже используют внедрение времени и правильные паттерны. Функции `now`, `after`, `sleep`, `readFile`, `fixedClock`, `stepClock`, `t.TempDir` и `t.Setenv` встречаются повсеместно. Исправление чаще сводится к распространению существующего приёма на соседний тест, а не к новому дизайну.
5. В git есть зафиксированный инцидент `c316617c` `fix(server): remove port-dial race from lifecycle stop test`. Команда уже сталкивалась с гонкой за ephemeral port. Похожий паттерн остался в `freePort` в e2e и в `TestLifecyclePortAlreadyInUse` в lifecycle_test.go.

## 1. Метод и границы выборки

Каталоги поиска: `internal`, `cmd`, `tests`, `evals`, `tools`. Шаблоны: `time.Sleep`, `time.After`, `time.Now`, `go func`, `t.Parallel`, `math/rand`, `os.Setenv`/`t.Setenv`, `os.Chdir`, `os.MkdirTemp`, `t.TempDir`, `net.Listen`, `net.Dial`, `httptest.NewServer`, а также присваивание переменных уровня пакета внутри тестов.

Что не считается риском. Большинство вхождений `time.Now()` в тестах безопасно. Тест вычисляет `now` один раз и передаёт его в чистую функцию или в фиктивные часы. Примеры: `internal/domain/transition_test.go`, `internal/infra/ladder/transitions_test.go`, `internal/infra/store/snapshots_test.go`, `internal/app/taskcli/service_test.go`. Эти тесты детерминированы и трогать их не нужно.

Что найдено, но не включено в топ:

- `cmd/beacon/watch_test.go:787` и `internal/infra/mcp/tool_get_result_test.go:1086` запускают горутину и ждут результата через `time.After(2s)` или `time.After(3s)`. Горутины завершаются по `context.CancelFunc`, поэтому риск средний, но время ожидания фиксированное.
- `internal/infra/webhook/receiver_test.go:247` ждёт сигнал от горутины через `time.After(2s)`. Сигнал есть, гонки в проверке нет, остаётся только жёсткий таймаут.
- `tools/beacon-gates/gate/tool_contract_test.go:203` подменяет `os.Stdout` и восстанавливает его через `defer`. Восстановление есть, но при панике или параллельных тестах в этом пакете состояние утечёт.
- `internal/tui/client_test.go:256` подменяет поле `app.render` без восстановления. Объект `app` локальный, поэтому утечки между тестами нет.
- `internal/infra/opencode/server_test.go:99` использует фиксированный порт `49374` в файле сервиса. Тест не слушает этот порт, поэтому риск невелик, но константа может конфликтовать с реальным сервисом на машине.

## 2. Кандидаты по категориям

### 2.1 Реальное время и опрос

`tests/e2e/e2e_test.go:340`, `:350`. Старт `opencode serve`: тест читает лог с диска, ищет регуляркой `server password` и `server listening`, спит 200 мс в цикле с дедлайтом 30 с. Файл может обновляться порциями, а запись в лог может отставать от готовности сервера. Фикс: сканировать stdout/stderr процесса через канал и ждать конкретную строку, а не опрашивать файл. Дедлайн передать через `context.WithTimeout`.

`tests/e2e/e2e_test.go:556`, `:562`, `:598`, `:631`, `:646`, `:662`. Четыре цикла ожидания факта в `events.jsonl` с `time.Sleep` от 200 до 500 мс. Каждый цикл останавливается по `time.Now().After(deadline)`. На грузной машине факт может прийти после дедлайта. Фикс: заменить опрос подпиской на событие плагина, либо вынести `deadline` в параметр и увеличить его в CI. Минимальный шаг: один общий хелпер `waitForFact(t, path, predicate, ctx)` вместо четырёх копий.

`tests/chaos/duplicate-delivery_test.go:213`, `:225`. `TestIndeterminateNoRetry` крутится `proofWindow` с шагом `proofWindow/10` и вызывает `time.Now()` внутри. Тест проверяет отрицательный факт: resends должны быть нулевыми. Длительность цикла зависит от нагрузки, поэтому на медленной машине он сделает меньше итераций, а на быстрой больше. Фикс: число итераций сделать фиксированным, а время исключить. Например `for attempt := 0; attempt < 10; attempt++`, и передавать в `deliverPointer` фиксированную метку времени.

`cmd/beacon/daemon_touch_test.go:80`, `:87`, `:93`. `TestRunDaemonHeartbeatTicksAndStops` считает тики через `heartbeatRecorder.snapshot()` раз в мс, затем спит 20 мс и проверяет, что тиков не прибавилось. Проверка «после отмены тиков нет» всегда основана на том, что 20 мс достаточно для проявления лишнего тика. Фикс: внедрить источник тиков в `runDaemonHeartbeat` и отсылать тики в буферизованный канал, который тест читает. Отсутствие тика проверять как `select` с `default` после того, как горутина подтвердила выход через `done`.

`internal/infra/store/observability_test.go:497`, `:502`. `waitForActiveWaiters` опрашивает `g.ActiveWaiters(key)` раз в мс до дедлайта 2 с. Фикс: добавить в `SharedGate` канал изменения числа ожидающих и ждать на нём. Альтернатива: внедрить часы в gate и проверять условие через детерминированные часы.

`internal/infra/mcp/tool_wait_result_test.go:1247`, `:1249`, `:1257`. Тест на 15 участников ждёт `entered.Load()`, потом спит 250 мс, чтобы остальные успели присоединиться. Это чисто временная гарантия. Фикс: сервис должен открывать число присоединившихся через `JoinedWaiters()`, тест ждёт `JoinedWaiters() == units`, затем закрывает timeout. Сон 250 мс удалить.

`internal/infra/httpapi/server_test.go:280`. Цикл чтения heartbeat из SSE-потока с дедлайтом по `time.Now()`. Фикс: передать `ctx` с таймаутом в `reader.ReadString` через обёртку, либо использовать `t.Context()`, если версия Go это позволяет. Дедлайн 2 с оставить как страховку, но не как главный механизм.

`internal/infra/server/lifecycle_test.go:284`, `:289`. `TestLifecycleRunStopsOnContext` опрашивает `lc.State()` каждые 5 мс. Фикс: добавить `WaitForState(ctx, StateRunning)` в `Lifecycle` и ждать на канале. Рядом уже исправленный `TestLifecycleStopIsIdempotent` показывает правильный подход: проверять `State()`, а не сетевой эффект.

### 2.2 Горутины и фиксированные таймауты

`internal/tui/socket_test.go:66`. `nextEvent` ждёт событие `time.After(2s)`. Сокетный сервер и клиент работают в одной машине, поэтому 2 с избыточно, но при перегрузке CI событие может опоздать. Фикс: привязать дедлайн к `t.Context()` и добавить `defer cancel()`.

`internal/infra/webhook/receiver_test.go:264`, `:269`. Два `time.After(2s)` вокруг горутины, которая вызывает `h.ServeHTTP`, и фонового рекордера. Фикс: заменить на `context.WithTimeout`, общий для обоих ожиданий.

`internal/infra/mcp/tool_wake_test.go:3019`. `awaitDoorbell` ждёт 3 с. Вокруг него несколько тестов с `assertDoorbellQuiet` через `select` с `default` в `:3025`. Проверка тишины по `default` не гарантирует, что сообщение не придёт через миллисекунду. Фикс: для негативных проверок закрыть контекст и дождаться завершения горутины через канал, затем проверять, что `Listen` вернул `context.Canceled`, а не тишину в момент проверки.

`internal/infra/mcp/tool_get_result_test.go:1086`, `:1090`. `task.Wait(..., 5*time.Second)` в горутине и ожидание результата через `time.After(2s)`. Несовпадающие дедлайты. Фикс: сделать дедлайн `Wait` параметром и установить его равным ожиданию теста.

`internal/infra/mcp/verdict_source_test.go:52`, `:63`, `:160`, `:248`. Четыре `time.After` на каналах, связанных с подпиской. Фикс: единый хелпер с контекстом и таймаутом из `t.Context()`.

`cmd/beacon/watch_w8b_test.go:146`, `:160`. `runWatch` в горутине, ожидание через `time.After(2s)`. Глобалы `watchLiveContext` и `watchLiveRunner` подменяются в `w3141Harness` и восстанавливаются в `t.Cleanup`. Риск средний, состояние восстанавливается. Фикс: передавать зависимости в `runWatch` через структуру, а не через переменные уровня пакета.

### 2.3 Сеть и порты

`tests/e2e/e2e_test.go:132`. `freePort` открывает `net.Listen("127.0.0.1:0")`, узнаёт порт, закрывает слушатель и возвращает номер. Между закрытием и повторным bind другой процесс может занять порт. Это тот же класс ошибки, что зафиксирован в `c316617c`. Фикс: возвращать сам слушатель и передавать его в процесс через fd, либо не закрывать слушатель до момента, когда `opencode serve` уже готов принять порт.

`internal/infra/opencode/server_test.go:67`, `:70`, `:89`. `startHealthServer` слушает реальный TCP-порт, выбранный продакшн-кодом, и подменяет `httptest.Listener`. Дополнительный риск в `:184`, где тест сам вызывает `net.Listen` на `req.host:req.port`. Фикс: внедрить `listen func(network, addr string) (net.Listener, error)` в `EnsureServer` и вернуть in-memory listener в тесте.

`internal/infra/server/lifecycle_test.go:255`. `TestLifecyclePortAlreadyInUse` держит блокирующий слушатель, чтобы проверить отказ `Start()`, затем закрывает его и сразу вызывает `Start()` снова. Освобождённый порт может быть переиспользован. Фикс: либо убрать повторный `Start()` на том же адресе, либо использовать адрес `127.0.0.1:0` и считывать фактический адрес у первого `Start()`.

### 2.4 Глобальное состояние

`cmd/beacon/main_test.go:542`. `t3145RestorePorts` сохраняет три переменные: `watchLiveSource`, `spawnSessionSpawner`, `deliverTarget`. Он не сохраняет `installedServerEnsurer`. Тесты в `:723`, `:750` вызывают `SetServerEnsurer(nil)` и в `t.Cleanup` снова пишут `nil`. Если до них в пакете запустился тест, который поставил ensurer, его значение будет потеряно. Это прямой источник зависимости от порядка. Фикс: расширить хелпер до всех четырёх портов и не писать `nil` в cleanup, а восстанавливать сохранённое значение.

`cmd/beacon/watch_test.go:770`-`:800`. Один хелпер подменяет пять переменных уровня пакета сразу: `watchLiveRunner`, `watchReconnectDelay`, `watchSnapshot`, `watchLiveContext`, `SetLiveSource`. Восстановление есть, но все тесты пакета делят это состояние. Фикс: сгруппировать зависимости в структуру `watchDeps` и передавать её в `runWatch`.

`cmd/beacon/deliver_test.go:44`, `:511`, `:662`. Аналогичная подмена `deliverSnapshot`, `deliverTarget`, `deliverSleep`, `resolvePiProbe`. Восстановление через `t.Cleanup` корректное. Риск ниже, чем в `main_test.go`, но при добавлении нового теста легко забыть вызов хелпера.

`internal/infra/opencode/server_test.go:47`. `installStartServer` подменяет `startServer` уровня пакета с восстановлением. Риск средний, та же причина: зависимость от состояния пакета.

### 2.5 Файловая система

`tests/e2e/e2e_test.go:79`, `:88`. `tmpRoot()` выбирает `/tmp/opencode` как общий корень, `newBase` создаёт каталог через `os.MkdirTemp` и не регистрирует `t.Cleanup`. После падения теста каталог остаётся. Разные e2e-тесты могут видеть чужие остатки. Фикс: `t.TempDir()` с явным указанием базового каталога через `TMPDIR` в окружении процесса, либо `os.MkdirTemp` плюс `t.Cleanup(os.RemoveAll)`.

`internal/tui/socket_test.go:19`. `os.MkdirTemp("", "bs39")` с ручным `t.Cleanup`. Исключение сделано из-за лимита длины пути Unix socket. Риск невелик, очистка есть. Можно оставить, но добавить комментарий с причиной, что уже сделано в `:15`.

`tests/adapter/receipts-wx.test.js:37`. `process.env.XDG_STATE_HOME` задаётся один раз на уровне модуля через `mkdtempSync`. Каталог не удаляется, а переменная влияет на все тесты файла. Фикс: создавать каталог в `beforeEach` и удалять в `afterEach`.

### 2.6 JavaScript-тесты плагина

`worktrees/z1/.opencode/plugins/beacon-bridge.js:951`-`:959`. Подробности в разделе 3.

`tests/adapter/s2-channel.test.js:63`. `waitFor` опрашивает предикат каждые 10 мс с лимитом 2000 мс. Это нормальный паттерн, но в комбинации с `:329`, где тест спит `3 * TEST_POLL_MS` и затем проверяет, что ничего не доставлено. Отрицательное утверждение на временном окне всегда хрупкое. Фикс: внедрить в `createS2Channel` функцию `tick` и `now`, вызывать тики вручную, а отрицательный случай проверять через лог событий с метками времени.

`tests/adapter/fail-open.test.js:61` и `tests/adapter/steer-prompt.test.js:82`. Фейковый поток событий ждёт 2 мс между итерациями. Это ускорение теста, но на перегруженном раннере 2 мс превращаются в неопределённое время. Фикс: заменить на обещание, которое разрешается по вызову теста, то есть на очередь, которую тест продвигает вручную.

## 3. Тесты, уже бывшие причиной CI-падений

### 3.1 beacon-bridge.js:959, гонка JSON.parse

Файл в рабочей копии: `worktrees/z1/.opencode/plugins/beacon-bridge.js:959` и его копии в `worktrees/z2` ... `worktrees/z14`. В `tests/e2e/fixtures/beacon-bridge.js` сейчас 133 строки, там этого кода нет.

Тест T3.16 проверяет доставку diff-комментария как steer. Он ставит событие в очередь, ждёт появления файла квитанции, останавливает плагин и сразу читает файл:

```js
const deadline = Date.now() + POLL_MS + 3000
while (Date.now() < deadline && !existsSync(receiptPath)) {
  await new Promise((tick) => setTimeout(tick, 5))
}
stop()
const line = JSON.parse(readFileSync(receiptPath, "utf8").trim())
```

Комментарий в `:949` говорит, что раньше был жёсткий sleep 30 мс и он давал ENOENT. Замена на `existsSync` убрала один симптом, но оставила гонку. `existsSync` сообщает о существовании файла, а не о завершённой записи. Квитанция пишется через `appendFileSync` в `deliver()` в `:72` и `:78`, а `stop()` в `:955` вызывает `ac.abort()`. Между созданием файла и полной записью строки, а также между abort и чтением есть окно. Пустой файл или обрезанная строка дают `SyntaxError` в `JSON.parse`, и тест падает не по своей причине.

Патч-идея: перенести чтение и разбор в тот же цикл ожидания. Пытаться `JSON.parse` внутри дедлайта, при ошибке делать ещё один tick. Требовать завершающий перевод строки в прочитанном тексте. Вызывать `stop()` только после успешного разбора. Ещё лучше: плагин должен писать отдельный уведомляющий факт о завершении доставки, а тест должен ждать этот факт через уже существующий механизм `ev`.

### 3.2 TestT3152InstallOpenCodePortsLeavesReaderClientUnwired

Файл: `cmd/beacon/main_test.go:748`. Тест проверяет, что при клиенте только для чтения POST-порты не ставятся:

```go
func TestT3152InstallOpenCodePortsLeavesReaderClientUnwired(t *testing.T) {
	t3145RestorePorts(t)
	t.Cleanup(func() { SetServerEnsurer(nil) })
	SetServerEnsurer(nil)
	port := &t3145Port{}
	installOpenCodePorts(port)
	if spawnSessionSpawner != nil || deliverTarget != nil || installedServerEnsurer != nil {
		t.Fatalf(...)
	}
}
```

Хелпер `t3145RestorePorts` в `:542` сохраняет только `watchLiveSource`, `spawnSessionSpawner` и `deliverTarget`. Переменная `installedServerEnsurer` в него не входит. Соседний тест `TestT3152InstallOpenCodePortsInstallsPostAndFallbackPorts` в `:721` ставит ensurer через `installOpenCodePorts` и восстанавливает его в `nil`, а не в прежнее значение. Порядок следования тестов в пакете определяет, что увидит тест в `:756`. Это классическая зависимость от порядка на глобалах.

Патч-идея: расширить `t3145RestorePorts` до всех четырёх портов, сохранить `installedServerEnsurer` и восстанавливать сохранённое значение. Полное решение: заменить четыре переменные уровня пакета на один `opencodePortRegistry`, который `installOpenCodePorts` получает аргументом. Тогда тест создаёт свежий реестр и вообще не трогает глобалы.

### 3.3 TestLifecycleStopIsIdempotent, зафиксированный в git

Коммит `c316617c` `fix(server): remove port-dial race from lifecycle stop test`. Старый тест делал `net.DialTimeout` на адрес после `Stop()` и ожидал отказа. Освобождённый ephemeral port мог достаться другому тесту, поэтому успешное соединение ничего не доказывало. Исправление заменило сетевую проверку на `State() == StateStopped` и добавило ожидание выхода горутины `Serve` в `lifecycle.go`.

Этот случай важен как доказательство, что класс ошибок реально проявлялся в CI. Похожий паттерн остался в `tests/e2e/e2e_test.go:132` и `internal/infra/server/lifecycle_test.go:255`.

## 4. Топ-10: тест, риск, фикс, приоритет

| Тест | Риск | Фикс | Приоритет |
| --- | --- | --- | --- |
| `beacon-bridge.js:959`, T3.16, комментарий ревью | JSON.parse по файлу, созданному но не дописанному; `stop()` до чтения | Ждать полную строку с переводом строки внутри дедлайта, `stop()` после успешного разбора | P0 |
| `TestT3152InstallOpenCodePortsLeavesReaderClientUnwired`, `cmd/beacon/main_test.go:748` | Зависимость от порядка: хелпер не сохраняет `installedServerEnsurer` | Сохранять все четыре порта; лучше внедрить реестр аргументом | P0 |
| `e2e_test.go` startup, `tests/e2e/e2e_test.go:340` | Опрос лога каждые 200 мс, общий `/tmp`, `freePort` с TOCTOU, ID из UnixNano | Готовность по каналу stdout, `t.TempDir`, слушатель без close-rebind, фиксированные ID | P0 |
| `TestRunDaemonHeartbeatTicksAndStops`, `cmd/beacon/daemon_touch_test.go:71` | Два временных окна: опрос 1 мс и проверка отсутствия тиков 20 мс | Внедрить тикер, тики в канал, выход горутины через `done` | P1 |
| `TestSharedGateCoalesces...`, `internal/infra/store/observability_test.go:495` и `tool_wait_result_test.go:1230` | Опрос 1 мс и сон 250 мс ради «успели присоединиться» | Канал изменения `ActiveWaiters` или метод `JoinedWaiters()` | P1 |
| `TestWebhookConcurrentRedeliverySingleEffect`, `internal/infra/github/points_test.go:602` | `time.Sleep(1ms)` в fake source специально расширяет гонку | Барьер из канала: все 16 горутин доходят до source, затем продолжают одновременно | P1 |
| `TestLifecycleRunStopsOnContext`, `internal/infra/server/lifecycle_test.go:277` | Опрос `State()` каждые 5 мс, рядом остался паттерн из исправленной гонки | `WaitForState(ctx, StateRunning)` в `Lifecycle` | P1 |
| `s2-channel.test.js`, `tests/adapter/s2-channel.test.js:329` | Негативная проверка через сон `3 * TEST_POLL_MS` | Внедрить `now` и `tick`, проверять лог событий с метками времени | P1 |
| `startHealthServer`, `internal/infra/opencode/server_test.go:67` | Реальный TCP-порт, фиксированный порт 49374 в `:99`, подмена `httptest.Listener` | Внедрить фабрику слушателей в `EnsureServer`, порт 0 | P2 |
| `nextEvent`, `internal/tui/socket_test.go:66` и `socketPath` в `:19` | `time.After(2s)` и `os.MkdirTemp` вручную | Таймаут через `t.Context()`, `t.TempDir()` с коротким путём сокета | P2 |

## 5. Конкретные патч-идеи

### 5.1 Разбор квитанции в тесте плагина

```js
let line = null
while (Date.now() < deadline) {
  if (existsSync(receiptPath)) {
    const raw = readFileSync(receiptPath, "utf8").trim()
    if (raw) {
      try { line = JSON.parse(raw); break } catch { /* неполная запись */ }
    }
  }
  await new Promise((tick) => setTimeout(tick, 5))
}
stop()
assert.ok(line, "строка квитанции так и не стала полной")
```

Успешный `JSON.parse` и есть признак полной строки, поэтому отдельная проверка завершающего символа не нужна. Надёжнее, чтобы плагин писал завершающий перевод строки, но для одного JSON-объекта достаточно самого разбора.

### 5.2 Восстановление всех портов

```go
func t3145RestorePorts(t *testing.T) {
	t.Helper()
	prevLive, prevSpawn, prevDeliver, prevEnsurer :=
		watchLiveSource, spawnSessionSpawner, deliverTarget, installedServerEnsurer
	t.Cleanup(func() {
		SetLiveSource(prevLive)
		SetSessionSpawner(prevSpawn)
		SetDeliveryTarget(prevDeliver)
		SetServerEnsurer(prevEnsurer)
	})
}
```

Из тестов в `:723` и `:750` убрать `t.Cleanup(func() { SetServerEnsurer(nil) })`. Хелпер уже делает восстановление.

### 5.3 Внедрение тиков в daemon heartbeat

Сигнатура `runDaemonHeartbeat(ctx, recorder, target, interval)` заменяется на приём `ticks <-chan time.Time`. Продакшн создаёт `time.NewTicker(interval)`, тест создаёт канал и посылает два тика вручную. Проверка «после cancel тиков нет» превращается в `select` с `default` после получения `done`. Никаких `time.Sleep(20ms)`.

### 5.4 Объединение ожиданий без сна

Добавить в `WaitResultService` метод `JoinedWaiters() int`. Тест в `tool_wait_result_test.go:1247` заменяет оба цикла и сон 250 мс на ожидание `JoinedWaiters() == units` с дедлайтом из `t.Context()`. Аналогично для `SharedGate` в `observability_test.go:495`.

### 5.5 Отложенная очистка в e2e

`newBase` должен регистрировать очистку сразу после `os.MkdirTemp`:

```go
t.Cleanup(func() { _ = os.RemoveAll(base) })
```

ID вида `"beacon-e2e-" + tag + "-" + fmt.Sprint(time.Now().UnixNano())` в `:673`, `:895`, `:896`, `:929` заменить на фиксированные строки. `t.Name()` можно оставить только в логах. Уникальность уже обеспечивает `base`.

### 5.6 Барьер вместо Sleep в points_test

```go
var arrived atomic.Int64
release := make(chan struct{})
source := func(ctx context.Context, ev WebhookEvent) (domain.Snapshot, error) {
	if arrived.Add(1) == n {
		close(release)
	}
	<-release
	return domain.Snapshot{State: "merged"}, nil
}
```

Все 16 горутин доходят до source, и только после этого канал открывается. Синхронизация становится явной, а не основанной на 1 мс сна.

## 6. Что делать в первую очередь

Порядок по убыванию пользы на единицу правки:

1. Расширить `t3145RestorePorts` до четырёх портов. Правка на пять строк, убирает известную зависимость от порядка.
2. Перенести `JSON.parse` внутрь цикла ожидания в `beacon-bridge.js`. Убирает известный CI-инцидент.
3. Добавить очистку в `newBase` в e2e. Убирает мусор в `/tmp/opencode` после падений.
4. Заменить сон 250 мс в `tool_wait_result_test.go` на ожидание счётчика присоединившихся.
5. Добавить `WaitForState` в `Lifecycle` и переписать `TestLifecycleRunStopsOnContext`.

Все пять правок локальны. Обновления продакшн-контрактов требуют только `WaitForState` и счётчик joined waiters. Кто не хочет трогать продакшн-код, может начать с пунктов 1, 2 и 3.
