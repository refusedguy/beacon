# ADR 312: Полная проверка MergePolicy в cibridge до авто-мержа

Статус: proposed

## Контекст

internal/infra/cibridge/verdict_applier.go:106-109 на VerdictReady проверяет только current.MergePolicy.AutoMerge и сразу вызывает applyMerge. Поля RequireApproval, WaitFor и WaitForMain объявлены в internal/domain/types.go:244-249 и обрабатываются в internal/app/orchestrator/orchestrator.go:146-181. Вызов orchestrator.Run живёт только в CLI cmd/beacon/orchestrate.go:241 и в порту демона cmd/beacon/daemon.go:517-519, поэтому политика обходится при мерже из цикла.

## Решение

Проверять все условия MergePolicy внутри applyVerdict до вызова applyMerge. RequireApproval удерживает PR. WaitFor и WaitForMain проходят через DependencyResolver, при отсутствии резолвера или ошибке verification PR остаётся на wait. Мерж по VerdictReady без полной проверки политики запрещён.

## Последствия

Условия карточек получают силу в основном цикле, а не только в ручной команде. Резолвер ошибок не превращает в разрешение зависимости. Существующий auto-rebase и closeWatch сохраняют контракт.
