# Q-B2. Барьер singleflight без runtime.Gosched
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/github/batch_provider.go, internal/infra/github/batch_provider_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Детерминированный барьер вместо эвристики планировщика, честный учёт shared.
Контекст: Premise: u3 уже влил это изменение в PR #873. Задача на проверку факта и закрытие, а не на дубль кода.
Шаги: Проверить в main отсутствие runtime.Gosched в batch_provider_test.go, наличие newJoiningBatchGroup и Shared, закрыть карточку RS-EVAL-01.
Проверка: gofmt, go test -race -count=20 в CI.
Приёмка: Дубль не создан, карточка закрыта со ссылкой на PR #873.
