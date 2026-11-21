# ATX-07. Добавить WaitForState и убрать опрос в TestLifecycleRunStopsOnContext
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/server/lifecycle_test.go, internal/infra/server/lifecycle.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить опрос состоянияLifetime на ожидание события.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.1 и строка таблицы 155. internal/infra/server/lifecycle_test.go:277, :284 и :289 опрашивает lc.State() каждые 5 мс. Рядом остался паттерн из гонки, исправленной коммитом c316617c. Baseline: интервал опроса 5 мс.
Шаги: Добавить WaitForState(ctx, StateRunning) в Lifecycle и ждать на канале. Переписать TestLifecycleRunStopsOnContext на новый метод. Оставить проверку State() как в уже исправленном TestLifecycleStopIsIdempotent.
Проверка: CI зелёный. В lifecycle_test.go нет цикла с time.Sleep(5ms) и вызовом State().
Приёмка: Тест ждёт состояния через канал. Сетевой эффект не используется как признак готовности.
