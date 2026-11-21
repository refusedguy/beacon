# RS-EVAL-01. Заменить runtime.Gosched на барьер joiningBatchGroup
Premise: проверено 2026-11-21 по origin/main (база ветки 6ece091, после fetch e788b9c). Эвристика на месте: восемь вызовов runtime.Gosched стоят в цикле строк 676-678 теста TestPingMergeableConcurrentCallersShareResolvedState. Барьер joiningBatchGroup есть в том же файле, строки 208-236, и закрывает канал all в строке 231. Барьер подключён к полю provider.batchSF в строке 262. Это поле имеет тип интерфейса batchGroup, объявленного в internal/infra/github/batch_provider.go:96-98 и используемого в поле 111. Функция pingMergeable работает с другим полем: prSF объявлено конкретным типом singleflight.Group в batch_provider.go:112 и вызывается в строке 506 как `v, _, _ := b.prSF.Do(...)`. Флаг shared там отбрасывается. Чтобы заменить цикл Gosched ожиданием `<-group.all`, тест должен ввести барьер в prSF. Это требует смены типа поля prSF на интерфейс batchGroup в batch_provider.go, вне OwnerFiles карточки. Чтобы проверить Shared у последователя, надо ещё вернуть флаг shared из pingMergeable в строке 506 и поправить три его вызова в строках 238, 309 и 393. Все три правки лежат в batch_provider.go. Обход в одном тестовом файле означает скопировать замыкание pingMergeable в тест и перестать проверять рабочий код, что не закрывает приёмку. Статус todo сохранён. Код не писал, gofmt не применял, OwnerFiles не менял, дубль не создал, wiring и .beacon не трогал. Блокер возвращён оркестратору с именем файла internal/infra/github/batch_provider.go.
baseline: `grep -c 'runtime.Gosched' internal/infra/github/batch_provider_test.go` = 1, строка 677 внутри `for i := 0; i < 8; i++`; `grep -n 'type joiningBatchGroup'` = 214, `close(g.all)` = 231; `grep -n 'prSF' internal/infra/github/batch_provider.go` = 112, 294, 506; `grep -n 'v, _, _ := b.prSF.Do'` = 506; `grep -n 'type batchGroup\|batchSF batchGroup'` = 96, 111; `grep -n 'b.pingMergeable('` = 238, 309, 393; `git rev-parse --short origin/main` = e788b9c, база ветки 6ece091.
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/github/batch_provider_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать эвристику из восьми runtime.Gosched и дождаться регистрации всех вызывающих через существующий барьер.
Контекст: Premise: проверено 2026-11-21, эвристика всё ещё в тесте, хотя joiningBatchGroup уже есть в том же файле. baseline: `grep -n 'runtime.Gosched' internal/infra/github/batch_provider_test.go` = строка 677, `grep -n 'type joiningBatchGroup'` = строка 214. Источник: Martin Fowler про недетерминизм в тестах.
Шаги: Заменить цикл Gosched ожиданием канала all. Дополнительно проверять Shared у последователя, а не только счётчик запросов. Счётчик в один может пройти и при втором flight, если запрос попал в кэш.
Проверка: gofmt чистый. Тест в CI с -race и -count=20.
Приёмка: Тест проверяет логику singleflight, а не планировщик. Флак по этой причине устранён.

## Блокер

- 2026-11-21. Шаги карточки нельзя выполнить в OwnerFiles `internal/infra/github/batch_provider_test.go`.

  Барьер `joiningBatchGroup` работает через поле `batchSF` и тип `batchGroup` в `batch_provider.go:96-98`. Тест `TestPingMergeableConcurrentCallersShareResolvedState` вызывает `pingMergeable`, а тот использует поле `prSF` конкретного типа `singleflight.Group` (`batch_provider.go:112`, вызов в строке 506). Внедрить барьер в `prSF` нельзя без правки типа поля в `batch_provider.go`. Флаг `shared` в строке 506 отбрасывается, поэтому проверить Shared у последователя тоже нельзя без правки этого файла и трёх его вызовов в строках 238, 309 и 393.

  Статус todo сохранён. OwnerFiles не менял, код не писал, gofmt не применял, чужие файлы и wiring/.beacon не трогал. Блокер возвращён оркестратору: нужны OwnerFiles с `internal/infra/github/batch_provider.go` или переформулировка шагов под существующие поля.
