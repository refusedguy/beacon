# CLEAN-11. Удалить три не подключённых пакета app
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/app/worker/, internal/app/scheduler/, internal/testsystem/
Зависимости: 
Ссылка: https://github.com/refusedguy/beacon/pull/1182
Агент: нет
Время: нет
baseline: `rg -n --glob "*.go" "internal/app/worker|internal/app/scheduler|internal/testsystem" internal cmd tools tests test evals scripts` дал 0 импортов этих пакетов вне их каталогов. Единственная ссылка на scheduler в коде: комментарий `internal/infra/server/scheduler.go:26`, рядом лежит отдельная реализация Kind и Schedule. testsystem жив по `.github/workflows/ci.yml:145`, `test/system/scenarios/README.md`, `tests/scenarios/seeded.yaml` и `tools/beacon-gates/gate/config/layers.json:13`.
Цель: Удалить internal/app/worker, internal/app/scheduler и internal/testsystem после проверки, что у них нет живых держателей.
Контекст: Источник: docs/research/deadcode-audit.md, строки про worker, scheduler и testsystem. Соответственно 293, 546 и 302 не-тестовых строки, 0 импортёров. Карточки BS.14, BS.15, T3.237, BS.24, BS.29, BS.31, T3.238 закрыты, T3.341 ещё todo. Риск: средний. Нужен owner-decision: закрыть T3.341 и подтвердить, что закрытые карточки не предполагали проводку.
Шаги: Проверить импорты и связанные WIRING-карточки. Закрыть T3.341 через batch-apply. Удалить три каталога одним PR, если владелец подтвердит отказ.
Проверка: CI зелёный, поиск по internal, cmd, tools не находит импортов удалённых пакетов.
Приёмка: Три каталога удалены только после решения владельца. Карточка остаётся todo.

## Ход работы

PR #1182 удалил `internal/app/worker` и `internal/app/scheduler`. Вне этих каталогов нет импортов, которые ссылаются на них. В конфигах reachability.json, baseline.json, permanent.json, registry.json и docs-baseline.json ссылок нет. layers.json задаёт ранг для `internal/app` целиком, отдельных записей для worker и scheduler в нём нет.

`internal/testsystem` оставлен. Джоба `seed-spec` в `.github/workflows/ci.yml:145` запускает `go test ./internal/testsystem/...`. README в `test/system/scenarios/README.md` описывает формат сценариев и запуск, `tests/scenarios/seeded.yaml` задаёт seed-регресс, layers.json содержит запись для пакета. T3.341 остаётся todo. batch-apply не запускался, команды владельца на закрытие T3.341 не было.

Статус остаётся todo. Цель карточки удалить все три каталога, а testsystem жив. Для него нужно отдельное решение владельца.
