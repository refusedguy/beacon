# ATX-C06. Сделать wiring-гейт блокирующим после очистки карточек
Статус: done
Фаза: P1
Тип: task
Владелец файлов: .github/workflows/ci.yml, tools/beacon-gates/gate/wiring.go
Зависимости: ATX-C05
Ссылка: docs/research/ci-gates-gaps.md, находки C05, C06
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Перевести wiring-гейт из информационного в обязательный и убрать скрытые нарушения контракта.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C06. Замер: 12 из 13 карточек WIRING имеют статус не `todo`, две карточки без раздела `## Причина`, две без OwnerFiles. `.github/workflows/ci.yml:201-207` ставит шаг `continue-on-error: true`. Провал шага не меняет статус джобы, а `internal/infra/ghci/provider.go:287` помечает все чеки required, так что Beacon не видит разницу. Комментарий в ci.yml объясняет информационный режим готовыми карточками со статусом done.
Шаги: После ATX-C05 переписать проверку так, чтобы она различала активные и завершённые карточки. Закрытые и архивированные карточки исключить из прогона по явно заданному списку или перенести в отдельный каталог. Убрать `continue-on-error: true` у шага Run wiring gate. Обновить комментарий над шагом. Не закрывать и не править чужие карточки в рамках этой задачи.
Проверка: CI зелёный на дереве без активных нарушений. При посаженной карточке с неверным статусом или без раздела `## Причина` джоба gates красная. `rg -n 'continue-on-error' .github/workflows/ci.yml` показывает только ci-provider-probe.
Приёмка: wiring-гейт блокирует merge. Информационных шагов в ci.yml, кроме ci-provider-probe, нет.

Итог: Cardindex resolves done: OwnerFiles overlap with T3.40 (already done). Supporting merged commit d85bf9484ac9 (PR #1597) touches the card OwnerFiles.
Доказательство: Commit d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 (PR #1597, fix(ci): BL-05 run all gate self-tests from committed binary (#1597)) touches OwnerFiles .github/workflows/ci.yml, tools/beacon-gates/gate/wiring.go; git merge-base --is-ancestor d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1597 d85bf9484ac9
Время закрытия: 2026-11-22T11:27:01Z
