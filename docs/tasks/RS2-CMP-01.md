# RS2-CMP-01. Перепроверка мержа против свежего main
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/domain/types.go, internal/infra/github/batch_provider_test.go
Зависимости: 
Ссылка: PR #878
Агент: нет
Время: нет
Цель: Передать base SHA в MergePRWithState и добавить вердикт recheck до попытки мержа.
Контекст: Premise: RS-CMP-01 вернула блокер, база в domain.BaseBranchState.HeadSHAtypes.go:442 не доходит до bridge.go:846. baseline: `grep -n 'recheck' internal/domain/verdict.go` пусто.
Шаги: Передать base SHA в MergePRWithState, добавить recheck в domain.Verdict, обработать в applyMerge, тест на два PR с одним файлом.
Проверка: gofmt, go test и гейты в CI.
Приёмка: Вердикт ready подтверждён проверкой на свежем main.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #878 (https://github.com/refusedguy/beacon/pull/878, merge commit cae40f5) влит. `grep -n 'VerdictRecheck' internal/domain/types.go` = 43, `grep -n 'publishRecheck' internal/infra/cibridge/bridge.go` = 879, `grep -n 'BaseHeadSHA' internal/domain/types.go` = 409. PR #888 (commit 8532b5e) добавил причину stale_base_after_main_fix для ребаза до мержа. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
