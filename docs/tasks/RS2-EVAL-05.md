# RS2-EVAL-05. Отдельная обязательная проверка для Broken
Статус: done
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/commands.go, tools/beacon-gates/gate/exceptions.go, docs/RULES-CHECK-CONTRACT.md, .github/workflows/ci.yml
Зависимости: 
Ссылка: PR #871
Агент: нет
Время: нет
Цель: Передавать код 2 без преобразования в warn и сделать отдельную mandatory job, падающую на Broken.
Контекст: Premise: RS-EVAL-05 зафиксировала, что commands.go:49-73 ставит status warn при любом ненулевом коде, а BROKEN уже описан в контракте.
Шаги: Пробросить код 2, добавить джобу, зафиксировать контракт, тест на skipped и neutral.
Проверка: gofmt, go test и гейты в CI.
Приёмка: Отказ инструмента не выглядит зелёной проверкой.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #871 (https://github.com/refusedguy/beacon/pull/871, merge commit 32a012b) влит. `grep -n 'R-TOOL-BROKEN' cmd/beacon/commands.go` = 73, `grep -n 'rules-broken' .github/workflows/ci.yml` = 247. Код выхода 2 даёт вердикт BROKEN и блокировщик вместо WARN, отдельная джоба без continue-on-error падает на BROKEN. Контракт RULES-CHECK-CONTRACT.md уже описывал BROKEN с commit 04862cc. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
