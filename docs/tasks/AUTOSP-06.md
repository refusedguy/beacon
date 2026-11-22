# AUTOSP-06. Единый гейт запуска перед спавном
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/app/taskcli/service.go, internal/app/taskcli/service_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Собрать все проверки допуска в один сервис, пригодный для вызова из daemon.
Контекст: Источник: docs/research/audit-01-autospawn.md, цепочка пункт 2. internal/app/taskcli/service.go:351 CheckLaunch сравнивает OwnerFiles, :368 PlanLaunch читает карточки, :392 пропускает done и dropped. cmd/beacon/task.go:648 вызывает проверку только вручную. internal/infra/github/points.go:433 PreflightGuard и internal/domain/orchestrator.go:84 CanAcceptTask готовы, но в гейте не участвуют. baseline: вызовов CheckLaunch вне CLI и тестов нет.
Шаги: Расширить LaunchAssignment полем статуса и добавить проверку пересечения с активными in_progress. Добавить метод, который принимает решения PreflightGuard и счётчик слотов и возвращает причину отказа. Не читать GitHub и не открывать БД внутри сервиса. Обновить тесты service_test.go.
Проверка: CI зелёный. Тест на пересечение с in_progress отказывает. Тест с quota_exhausted отказывает до запуска. Тест с полным лимитом слотов отказывает.
Приёмка: Автоспавнер вызывает один метод и получает решение Allowed с причиной.
