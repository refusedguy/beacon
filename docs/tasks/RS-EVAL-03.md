# RS-EVAL-03. Именованные таймауты и smoke тест старта evalserver
Статус: done
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

## Подтверждение
- Premise перепроверен 2026-11-21 по свежему origin/main (527084c). Прежний блокер снят: реализация влита PR #879, коммит 74ad82de, merged 2026-11-21. Baseline от 89276ef устарел. В evals/cmd/smoke.go на main определены DefaultStartTimeout (60s) и DefaultTerminateTimeout (2s) с env BEACON_EVAL_START_TIMEOUT и BEACON_EVAL_TERMINATE_TIMEOUT. Парсер Duration сохраняет значение по умолчанию при пустом или неверном env. Проверка smoke.Check ждёт handshake MCP через контекст с дедлайтом и не использует sleep. Причина провала попадает в сообщение об ошибке через Reason: start_timeout отличает медленный старт от connect_error. В evals/main.go на main оба таймаута читаются через smoke.Duration, terminateTimeout передаётся в CommandTransport.TerminateDuration, литералов 60 и 2 секунд в run() больше нет. evals/run.sh экспортирует оба env со старыми значениями по умолчанию. Открытых PR по карточке нет, дубля нет. Статус изменён с todo на done со ссылкой на PR #879. OwnerFiles не менял, код не писал, gofmt не применял, чужие файлы, wiring и .beacon не трогал, правка только текстовая.
