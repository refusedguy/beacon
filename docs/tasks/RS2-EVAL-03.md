# RS2-EVAL-03. Именованные таймауты и smoke тест evalserver
Статус: done
Фаза: P3
Тип: task
Владелец файлов: evals/main.go, evals/run.sh, evals/cmd/smoke.go
Зависимости: 
Ссылка: PR #879
Агент: нет
Время: нет
Цель: Вынести 60s и 2s из run() в константы или env и отделить медленный старт от логического провала.
Контекст: Premise: RS-EVAL-03 вернула блокер, литералы в evals/main.go:191 и :206, OwnerFiles карточки их не содержали.
Шаги: Константы, env, smoke тест старта, тест на таймаут как отдельную причину.
Проверка: gofmt, eval runner в CI.
Приёмка: Увеличение таймаута не выглядит как исправление логики.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #879 (https://github.com/refusedguy/beacon/pull/879, merge commit 74ad82d) влит. `grep -n 'DefaultStartTimeout' evals/cmd/smoke.go` = 21, `grep -n 'BEACON_EVAL_START_TIMEOUT' evals/run.sh` = 17, `grep -n 'SMOKE_start_reason' evals/main.go` = 1043. Медленный старт получает причину start_timeout отдельно от connect_error. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
