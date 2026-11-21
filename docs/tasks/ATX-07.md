# ATX-07. Добавить WaitForState и убрать опрос в TestLifecycleRunStopsOnContext
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/server/lifecycle_test.go, internal/infra/server/lifecycle.go
Зависимости: 
Ссылка: 
Агент: exe90
Время: 2026-11-21T17:51:09Z
Цель: Заменить опрос состоянияLifetime на ожидание события.
Контекст: Источник: docs/research/flaky-tests-audit.md, раздел 2.1 и строка таблицы 155. internal/infra/server/lifecycle_test.go:277, :284 и :289 опрашивает lc.State() каждые 5 мс. Рядом остался паттерн из гонки, исправленной коммитом c316617c. Baseline: интервал опроса 5 мс.
Шаги: Добавить WaitForState(ctx, StateRunning) в Lifecycle и ждать на канале. Переписать TestLifecycleRunStopsOnContext на новый метод. Оставить проверку State() как в уже исправленном TestLifecycleStopIsIdempotent.
Проверка: CI зелёный. В lifecycle_test.go нет цикла с time.Sleep(5ms) и вызовом State().
Приёмка: Тест ждёт состояния через канал. Сетевой эффект не используется как признак готовности.

Итог: Already landed in PR #1216 (commit efb55590cf62022713a9d1af1d30e065ec40ae47): Lifecycle.WaitForState added and polling removed in TestLifecycleRunStopsOnContext
Доказательство: PR #1216 merged 2026-11-21T16:26:32Z; commit efb55590cf62022713a9d1af1d30e065ec40ae47 changes internal/infra/server/lifecycle.go and lifecycle_test.go; git merge-base --is-ancestor efb55590cf62022713a9d1af1d30e065ec40ae47 origin/main exit 0; no open duplicate PR
Причина: already landed in PR #1216 efb55590
Время закрытия: 2026-11-21T17:55:26Z