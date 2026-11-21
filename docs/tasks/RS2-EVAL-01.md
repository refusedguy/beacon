# RS2-EVAL-01. Заменить runtime.Gosched на барьер в singleflight
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/github/batch_provider.go, internal/infra/github/batch_provider_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сменить prSF на интерфейс batchGroup, вернуть shared из pingMergeable и дождаться регистрации вызывающих.
Контекст: Premise: RS-EVAL-01 вернула блокер, восемь Gosched на строке 677, барьер подключён к batchSF. baseline: `grep -n 'runtime.Gosched'` = 677.
Шаги: Поменять тип prSF, возвращать shared, поправить вызовы в строках 238, 309, 393, убрать Gosched.
Проверка: gofmt, go test -race -count=20 в CI.
Приёмка: Тест проверяет логику, а не планировщик.
