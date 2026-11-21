# RS-EVAL-03. Именованные таймауты и smoke тест старта evalserver
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: evals/run.sh, evals/cmd/smoke.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
baseline: 2026-11-21 по origin/main 89276ef. `grep -n '60\*time.Second\|2 \* time.Second' evals/main.go` = строки 191 и 206, по одному вхождению. `grep -c '60\*time.Second\|60 \* time.Second' evals/main.go` = 1, `grep -c '2 \* time.Second\|2\*time.Second' evals/main.go` = 1. `git diff origin/main..HEAD -- evals/main.go` = 0 строк, файл не отличается от main. В `evals/run.sh` таймаутов нет. Среди `os.Getenv` в evals/main.go нет таймаутной переменной. `ls evals/cmd/smoke.go` = файла нет. В evals/cmd/evalserver/main.go три режима проверок: --drift-check, --severity-map-check, --report-check. Сигнала готовности и стартового smoke там нет: `grep -rni 'ready\|health\|timeout\|Sleep' evals/cmd/evalserver/*.go` = 0 совпадений. Подключение к серверу и оба таймаута живут в run() в evals/main.go:191-215.
Цель: Вынести 60 секунд и 2 секунды в именованные константы или env и добавить отдельный smoke тест старта сервера.
Контекст: Premise: проверено 2026-11-21, таймауты зашиты литералами в run(). baseline: `grep -n '60 \* time.Second\|2 \* time.Second' evals/main.go` находит оба. Источник: Fowler про голые sleep и тесты с таймаутом.
Шаги: Создать константы с понятными именами. Добавить env для переопределения в CI. Smoke тест отличает медленный старт от логического провала. Не использовать sleep как синхронизацию.
Проверка: gofmt чистый. Скрипт и smoke тест зелёные в CI.
Приёмка: Увеличение таймаута не выглядит как исправление логики. Причина провала видна в отчёте.

## Блокер

- 2026-11-21. Оба таймаута живут в чужом OwnerFiles.

  Литерал 60 секунд стоит в `evals/main.go:191` внутри run(). Литерал 2 секунды стоит в `evals/main.go:206` в `CommandTransport.TerminateDuration` внутри того же run(). Файл `evals/main.go` не входит в OwnerFiles карточки `evals/run.sh` и `evals/cmd/smoke.go`. Чтобы заменить литералы на константы или env, нужно править этот файл. Я его не трогал.

  В `evals/run.sh` таймаутов нет, поэтому вынести 60 и 2 секунды из скрипта нельзя. Env, который читал бы эти значения, в evals/main.go отсутствует. Добавление env требует правки evals/main.go.

  Smoke тест старта evalserver тоже упирается в evals/main.go. Сервер поднимается через `client.Connect` с `CommandTransport` в evals/main.go:191-215. Отличить медленный старт от логического провала можно только по этому пути подключения и инициализации. В evals/cmd/evalserver/main.go готового сигнала готовности или отдельного стартового режима нет. Новый файл evals/cmd/smoke.go может запустить evalserver, но не знает про таймауты из run() и дублирует стартовую последовательность. Без правки evals/main.go тест не проверит тот таймаут, который описан в карточке.

  Поведения в main нет. origin/main 89276ef содержит те же два литерала на строках 191 и 206. PR по RS-EVAL-03 в репозитории нет. Дубль не создавал.

  Статус todo сохранён. Код не писал, gofmt не применял, go test не запускал, OwnerFiles не менял, чужие файлы, wiring и .beacon не трогал. Блокер возвращён оркестратору с именем файла `evals/main.go`. Для карточки нужны OwnerFiles с `evals/main.go`, либо переформулировка шагов под уже существующие проверки в evals/cmd/evalserver.
