# Q-B4. Вторая линия защиты wake и токены слотов
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/domain/types.go, internal/infra/ladder/transitions.go, internal/infra/ladder/escalation.go, cmd/beacon/daemon.go, internal/app/orchestrator/decision.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: claimedAt проверяется в daemon, ступени ladder в обязательстве, токен слота подключён к запуску задач.
Контекст: Premise: u2 влил ступени в PR #877, t2 влил модель токена в #851. Осталось подключить claimedAt в cmd/beacon/daemon.go:359 и связать токен с реальным спавном.
Шаги: Прокинуть порт claimedAt, подключить токен к spawn, тест на повторный сигнал и задержанный артефакт.
Проверка: gofmt, go test в CI.
Приёмка: Повторный сигнал не открывает второй слот, мёртвый claimedAt не будит.
