# RS-EVAL-01. Заменить runtime.Gosched на барьер joiningBatchGroup
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
