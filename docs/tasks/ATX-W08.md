# ATX-W08. Не передавать отмену лидера участникам общего CI-ождания
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/observability.go, internal/infra/store/observability_test.go
Зависимости: 
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W08
Агент: нет
Время: нет
Цель: Дать раунду `SharedGate` собственный контекст и вести счётчик активных участников.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Общее CI-ождание». `SharedGate.Wait` в `internal/infra/store/observability.go:505-549` обещает, что отмена одного участника не трогает раунд. Лидер передаёт свой ctx в `fn`, поэтому его отмена останавливает общую работу и все присоединившиеся получают ошибку лидера. Поле `waiters` увеличивается на входе и не уменьшается на ветке `ctx.Done()`, поэтому `ActiveWaiters` показывает сумму присоединившихся. PR #1217 заменил фиксированный `sleep` счётчиком только в тесте, production-код не менялся. Baseline: тест `TestSharedCIWaitCoalescesOneCI` проверяет число вызовов `fn`, но не проверяет отмену лидера.
Шаги: Создавать для раунда производный ctx с собственным таймаутом, не привязанным к отмене лидера. Уменьшать `waiters` под мьютексом при выходе через `ctx.Done()`. Передавать участникам результат раунда даже после ухода лидера. Добавить тест с отменой лидера внутри `fn` и живым ctx участника, а также проверку `ActiveWaiters` после ухода.
Проверка: CI зелёный. Участник получает результат `fn` или ошибку раунда, но не `context.Canceled` лидера. Счётчик возвращается к нулю после завершения всех участников.
Приёмка: Один лидер не может сорвать ожидание остальных, метрика участников соответствует текущему числу ожидающих.

Итог: Cardindex resolves done: OwnerFiles overlap with T1.9 (already done). Supporting merged commit 6325f31b0b4b (PR #1582) touches the card OwnerFiles.
Доказательство: Commit 6325f31b0b4b086fd112b3a479b4546af44a741e (PR #1582, feat(store): expose SharedGate state through store API (OBS-04) (#1582)) touches OwnerFiles internal/infra/store/observability.go, internal/infra/store/observability_test.go; git merge-base --is-ancestor 6325f31b0b4b086fd112b3a479b4546af44a741e origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1582 6325f31b0b4b
Время закрытия: 2026-11-22T11:28:03Z
