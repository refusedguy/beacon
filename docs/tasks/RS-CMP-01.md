# RS-CMP-01. Проверка мержа против свежего main
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/github/batch_provider.go, internal/infra/github/rebase.go, internal/infra/github/rebase_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Перепроверять mergeability сразу перед мержем и возвращать rebase при расхождении baseRef, а не после неудачного мержа.
Контекст: Premise: проверено 2026-11-21, MergePRWithState использует состояние из того же батча, полученного до решения. baseline: `grep -n 'func (b \*BatchProvider) MergePRWithState' internal/infra/github/batch_provider.go` = строка 371. Источник: Mergify merge queue и GitHub merge queue.
Шаги: Добавить перечтение baseRef в MergePRWithState. При расхождении возвращать отдельный результат, который оркестратор трактует как повторную проверку. Тест на два PR, меняющих один файл: первый мержится, второй должен провалиться на подготовке поверх свежего main.
Проверка: gofmt чистый. Тесты в CI. Негативный контроль: старый снимок больше не даёт ready.
Приёмка: Вердикт ready подтверждён проверкой на актуальном main. Конфликт не превращается в мерж с последующим rebase.
