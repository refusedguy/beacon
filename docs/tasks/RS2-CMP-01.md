# RS2-CMP-01. Перепроверка мержа против свежего main
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/domain/types.go, internal/infra/github/batch_provider_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Передать base SHA в MergePRWithState и добавить вердикт recheck до попытки мержа.
Контекст: Premise: RS-CMP-01 вернула блокер, база в domain.BaseBranchState.HeadSHAtypes.go:442 не доходит до bridge.go:846. baseline: `grep -n 'recheck' internal/domain/verdict.go` пусто.
Шаги: Передать base SHA в MergePRWithState, добавить recheck в domain.Verdict, обработать в applyMerge, тест на два PR с одним файлом.
Проверка: gofmt, go test и гейты в CI.
Приёмка: Вердикт ready подтверждён проверкой на свежем main.
