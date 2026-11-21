# ATX-C01. Включить required status checks для ветки main
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: .github/workflows/README.md
Зависимости: 
Ссылка: docs/research/ci-gates-gaps.md, находка C01; PR #1239, #1259
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Сделать обязательные проверки действительно блокирующими merge в main.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C01. Замер: `gh api repos/refusedguy/beacon/branches/main/protection` возвращает HTTP 404 и текст `Branch not protected` на `origin/main` = `df88de4d`. PR #1239 прямо сообщает, что branch protection не настроен. Удаление `continue-on-error` в #1259 изменило статус джобы docs, но не создало required check. `internal/infra/ghci/provider.go:287` помечает все чеки из `gh pr checks` как required, поэтому разница между обязательной и информационной проверкой в вердикте Beacon не различается.
Шаги: Включить защиту ветки main через GitHub API. Добавить обязательные чеки build, test, seed-spec, js-test, gates, cardindex, rules-broken, registry-check, docs, gates-binary. Оставить e2e и ci-provider-probe вне списка. Зафиксировать список и порядок его обновления в `.github/workflows/README.md`.
Проверка: CI зелёный. `gh api repos/refusedguy/beacon/branches/main/protection --jq .required_status_checks.contexts` возвращает ровно десять перечисленных имён. PR с красным чеком build не мержится в main.
Приёмка: Красная обязательная проверка блокирует merge. Информационные проверки остаются видимыми, но не блокируют.
