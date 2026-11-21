# RS2-EVAL-01. Заменить runtime.Gosched на барьер в singleflight
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/github/batch_provider.go, internal/infra/github/batch_provider_test.go
Зависимости: 
Ссылка: PR #873
Агент: нет
Время: нет
Цель: Сменить prSF на интерфейс batchGroup, вернуть shared из pingMergeable и дождаться регистрации вызывающих.
Контекст: Premise: RS-EVAL-01 вернула блокер, восемь Gosched на строке 677, барьер подключён к batchSF. baseline: `grep -n 'runtime.Gosched'` = 677.
Шаги: Поменять тип prSF, возвращать shared, поправить вызовы в строках 238, 309, 393, убрать Gosched.
Проверка: gofmt, go test -race -count=20 в CI.
Приёмка: Тест проверяет логику, а не планировщик.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #873 (https://github.com/refusedguy/beacon/pull/873, merge commit 3bcfc01) влит. `grep -c 'runtime.Gosched' internal/infra/github/batch_provider_test.go` = 0, `grep -n 'prSF' internal/infra/github/batch_provider.go` = 112 с типом batchGroup, `grep -n 'v, _, shared := b.prSF.Do'` = 513. Тест ждёт `<-group.all` и проверяет shared у обоих вызывающих. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
