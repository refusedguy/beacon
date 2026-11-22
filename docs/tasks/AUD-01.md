# AUD-01. Запретить cibridge авто-мерж при RequireApproval/WaitFor/WaitForMain
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/verdict_applier.go, internal/infra/cibridge/verdict_applier_test.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Не допускать авто-мерж в cibridge, когда политика Watch требует одобрения или ожидания зависимостей.
Контекст: Источник: аудит fix/c18-a17, P0.1. `ReconcilePass` в cmd/beacon/mcp.go вызывает `bridge.Refresh` и не дёргает `orchestrator.Run`. В `internal/infra/cibridge/verdict_applier.go` ветка `domain.VerdictReady` проверяет только `current.MergePolicy.AutoMerge && b.merger != nil`, затем сразу вызывает `applyMerge`. `RequireApproval`, `WaitFor` и `WaitForMain` игнорируются. baseline: 0 проверок политики в `applyVerdict`.
Шаги: Добавить в `applyVerdict` guards: при `MergePolicy.RequireApproval==true` или непустых `WaitFor`/`WaitForMain` `applyMerge` не вызывать, Watch оставить для оркестратора через узкий порт проверки зависимостей из domain. Не слать WAKE при удержании. Добавить unit-тесты на отказ мержа для трёх случаев и на разрешение при чистом AutoMerge.
Проверка: CI зелёный. Тесты отказывают в мерже при requireApproval:true, непустом waitFor и waitForMain. Нет регресса на обычный AutoMerge. Никаких изменений cmd/beacon/mcp.go или orchestrator.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.

Итог: Already landed in #1448 (e724f0eca86582a9f2353257b06293c18023af89). The same fix landed under duplicate card AUTOSP-01 and touches the same OwnerFiles. No duplicate code needed.
Доказательство: PR #1448 commit e724f0eca86582a9f2353257b06293c18023af89 merge-base --is-ancestor e724f0eca86582a9f2353257b06293c18023af89 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1448 e724f0e
Время закрытия: 2026-11-22T11:29:08Z
