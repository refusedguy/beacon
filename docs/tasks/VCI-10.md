# VCI-10. Починка main снимает блокер без слияния PR

Premise: проверено 2026-11-23 по свежему `origin/main` `e589b3e49f292b7d6b8153bf3982b03db9a45830`. Реализация из PR 1771 уже влита. `git merge-base --is-ancestor f82897a594e71b09accbb657fc4ba694d951f659 origin/main` вернул 0. Код и тесты на HEAD подтверждают поведение из карточки. Дубль кода не нужен. OwnerFiles заняты открытыми PR, поэтому эта ветка не меняет ни один `.go`-файл.

Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go, internal/infra/cibridge/verdict_applier.go, internal/infra/cibridge/bridge_test.go
Зависимости: VCI-02, VCI-03
Ссылка: https://github.com/refusedguy/beacon/pull/1771; бэклог `/tmp/opencode/backlog/04-verdict-ci.md`
Агент: vci10-executor
Время: 2026-11-23T06:34:18Z

Цель: Зелёная база снимает блокер по старому инциденту без слияния PR. Повторный проход не создаёт второй эффект.

Контекст: До починки `ResolveBlockersByBaseGreen` вызывался только из `applyMerged`. Если main починить, а PR не слить, инцидент оставался открытым. Теперь `Refresh` вызывает resolver после цикла Watch, когда провайдер видел base с известными проверками и без провалов. Заблокированные Watch остаются в выборке опроса, поэтому их base наблюдается в том же цикле.

Шаги: Проверить premise по свежему `origin/main` и merge-коммиту PR 1771. Подтвердить в коде вызов resolver после цикла Watch, участие заблокированных Watch в опросе, фильтр открытых инцидентов и наличие тестов. Записать карточку со статусом done и доказательством. Исходный код не трогать.

Проверка: `.go`-файлы не менялись. `gofmt -l` по трём OwnerFiles не вывел ничего. Локальные `go test`, `go build` и гейты не запускались по правилам AGENTS.md. Полные проверки выполняет CI на PR.

Приёмка: Блокер снимается, подпись инцидента и wake-dedup VCI-02 сохраняются, генерация Watch двигается на один шаг, повтор безопасен, поведение прежнее. Все пункты подтверждены кодом и тестами на HEAD, которые перечислены ниже.

Причина: already_landed
Закрыл: vci10-executor
Время закрытия: 2026-11-23T06:34:18Z

## Подтверждение

- PR #1771 MERGED, merge SHA `f82897a594e71b09accbb657fc4ba694d951f659`. Проверка предка вернула 0.
- `internal/infra/cibridge/bridge.go:1193` вызывает `resolveBaseGreenBlockers` после цикла Watch и пропускает наблюдения без `ChecksKnown` или с упавшими проверками. `handleBlocked` не воссоздаёт инцидент в том же цикле.
- `internal/infra/cibridge/ci_refresh.go:38` держит заблокированные Watch в выборке, поэтому `fetchSnapshots` получает base для их инцидентов.
- `internal/infra/cibridge/verdict_applier.go:491` читает `BaseRef` Watch, вызывает `ResolveBlockersByBaseGreen` и вращает генерации через `rotateGenerations` на `:507`. Путь `applyMerged` использует тот же resolver на `:534`.
- `internal/infra/store/blockers.go:319` ищет только открытые инциденты с `status = open` на `:328` и `:336`. Повторный вызов не находит уже закрытый инцидент и не пишет вторую запись. Подпись инцидента строится на `:181`, фильтр проверок на `:81`. Wake по ключу `blocker:<incident.ID>` остаётся на `:736` и `:915` в `verdict_applier.go`.
- `internal/infra/cibridge/verdict_applier_test.go:142` `TestRefreshResolvesBlockerWhenBaseTurnsGreenWithoutMerge` снимает инцидент и блокер без MERGED PR, проверяет статус watching, пустые `Blocked` и `BlockerID`, приращение revision и одну ротацию генерации. `TestRefreshKeepsBlockerWhenBaseChecksUnknown` на `:215` фиксирует fail-closed путь.
- `internal/infra/store/blockers_test.go:247`, `:302` и `:358` покрывают зелёную базу, legacy `base_ref` и подпись без информационных проверок. `internal/infra/cibridge/bridge_test.go:865` `TestBridgeRepeatedMergeUnblocksOnce` подтверждает, что повторная обработка не даёт второй разблокировки.

OwnerFiles активно заняты: PR #2230 `fix/vci02` меняет `internal/infra/cibridge/verdict_applier.go`, PR #2193 `fix/bp06` меняет `internal/infra/cibridge/bridge.go`. Оба PR открыты. Этот PR не трогает исходный код и не откатывает их изменения. Ранее влитые PR 1768, 1769, 1770, 1532, 1917, 1565 и 1988 проверены через `git merge-base --is-ancestor`, все вернули 0.
