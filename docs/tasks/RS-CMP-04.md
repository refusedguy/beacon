# RS-CMP-04. Безопасность авто-мержа по обязательным проверкам
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, internal/infra/batchapply/apply_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
baseline: до правки `grep -n 'required :=' internal/infra/batchapply/apply.go` = строка 501, инлайн-условие `blocked || (len(checks) > 0 && green < len(required))`, тестов `applyMergePlan` = 0, SCAN-BLIND в `tools/beacon-gates/gate/gate.go:166`.
Цель: Не мержить PR с красными проверками внутри зелёной группы и требовать, чтобы обязательный чек был привязан к конкретному приложению.
Контекст: Premise: проверено 2026-11-21, applyMergePlan уже требует build, test, gates и gates-binary. GitHub принимает skipped и neutral как прохождение required check, поэтому одного зелёного статуса мало.
Шаги: Проверять, что все обязательные проверки присутствуют, а не только что нет красных. Различать отсутствие проверок у doc-only PR и потерю проверки у кодового PR. Зафиксировать политику привязки чека к GitHub App в контракте. Тесты на пропущенную проверку и на neutral.
Проверка: gofmt чистый. Тесты в CI. SCAN-BLIND сохранён.
Приёмка: PR с пропавшей проверкой удерживается. doc-only PR по paths-ignore по-прежнему проходит.

## Решение

2026-11-21. Инлайн-логику в `applyMergePlan` заменила чистая функция `evaluateChecks` в `internal/infra/batchapply/apply.go`. Doc-only PR без проверок проходит по paths-ignore. Кодовый PR обязан нести `build`, `test`, `gates` и `gates-binary` в состоянии SUCCESS или PASS. Пропущенная проверка даёт `holdMissingCheck`, не зелёная даёт `holdNotGreen`, поэтому причины удержания различаются. GitHub считает skipped и neutral прохождением required check, поэтому `greenCheckState` принимает только SUCCESS и PASS. Каждая проверка из списка разбирается, и зелёная группа не прячет один neutral загруз.

В `internal/infra/batchapply/apply_test.go` добавлены случаи: пропущенная проверка, neutral, skipped, failure, doc-only, все зелёные, дубль neutral внутри зелёной группы, красная необязательная проверка. `gofmt -w` применён к двум своим файлам. `go test` и `go build` не запускались, прогон идёт в CI на PR.

SCAN-BLIND в `tools/beacon-gates/gate/gate.go:166` не менялся. Шаг про привязку чека к GitHub App в контракте лежит в `docs/RULES-CHECK-CONTRACT.md`, вотчине RS-EVAL-05, и в этом PR не тронут. Дубля в main нет: старая логика тоже удерживала пропущенные и neutral-проверки, но смешивала причины и не имела тестов.
