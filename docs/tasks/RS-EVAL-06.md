# RS-EVAL-06. CI-only probes для сетевого пропуска R01
Статус: done
Фаза: P3
Тип: task
Владелец файлов: .github/workflows/ci.yml, docs/evals/ci-provider-probe.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Добавить отдельную джобу с реальным токеном на фикстурном репозитории, чтобы принятый пропуск R01 не стал постоянной слепой зоной.
Контекст: Premise: проверено 2026-11-21, R01 честно помечает, что CIProvider не опрашивается. baseline: `grep -n 'name:' .github/workflows/ci.yml` показывает пять джоб, ни одна не проверяет живой провайдер. Источник: Anthropic про баланс кейсов и negative controls.
baseline: 2026-11-21 по origin/main 89276ef. `git show origin/main:.github/workflows/ci.yml | grep -cE '^  [a-z0-9-]+:$'` = 6, из них `push:` и пять джоб build, test, js-test, gates, e2e. `git show origin/main:.github/workflows/ci.yml | grep -c 'secrets\.'` = 0. `grep -rn 'ci-provider-probe' origin/main` = 0 файлов. Живой провайдер в R01_accepted_skip не вызывается: evals/main.go:812 передаёт headSHA заглушкой и пишет WARN (evals/README.md:112). После правки `grep -cE '^  [a-z0-9-]+:$'` = 7, джоб шесть, `grep -c 'secrets\.'` = 2.
Шаги: Создать джобу с токеном из секретов и фикстурным PR. Проверить и позитивный, и негативный контроль. Не делать эту проверку обязательной для локального прогона. Задокументировать, как обновлять фикстуру.
Проверка: Только текст конфига и документа. Работоспособность джобы проверяется в CI.
Приёмка: Слепая зона R01 имеет покрытие и владельца.

## Подтверждение

- 2026-11-21. Сделано в OwnerFiles карточки. В `.github/workflows/ci.yml` добавлена джоба `ci-provider-probe` и триггер `workflow_dispatch`. Джоба читает `BEACON_CI_PROBE_TOKEN` из секретов в `GH_TOKEN` и `BEACON_GITHUB_TOKEN`, опрашивает фикстурный PR через те же вызовы `gh pr view` и `gh pr checks`, что делает `internal/infra/ghci.Provider`. Позитивный контроль проверяет `state=OPEN`, headSHA формата 40 hex и хотя бы одну зелёную проверку. Негативный контроль требует ошибки для несуществующего PR `999999`. Джоба не обязательна для локального прогона: `if: github.event_name == 'workflow_dispatch'` и `continue-on-error: true`.
- В `docs/evals/ci-provider-probe.md` записаны сценарий, оба контроля, таблица переменных, текущая фикстура `refusedguy/beacon-ci-fixture` PR `1` и семь шагов обновления фикстуры и токена. Владелец слепой зоны зафиксирован в разделе «Владелец».
- Чужие OwnerFiles, `wiring/`, `.beacon/` и `docs/tasks/` кроме этой карточки не трогал. Go-файлов нет, gofmt не применял, `go test` и `go build` не запускал. Проверен только текст конфига и документа по условию карточки. Работоспособность джобы проверится в CI при наличии секрета.
