# ATX-C04. Запускать reachability-гейт в CI
Статус: done
Фаза: P1
Тип: task
Владелец файлов: .github/workflows/ci.yml, .github/workflows/README.md
Зависимости: 
Ссылка: docs/research/ci-gates-gaps.md, находка C04
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Подключить зарегистрированный reachability-гейт к обязательному прогону CI.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C04. Замер: `rg -n 'reachability' .github/` возвращает 0 строк, хотя `tools/beacon-gates/main.go:24` регистрирует команду в бинарнике. Джоба gates в `ci.yml:188-196` прогоняет capabilities, layers-ast и layers-list. В `tools/beacon-gates/gate/config/reachability.json` лежат 23 permanent-строки, из них 20 с правилом REACH-UNWIRED и ссылкой на Wave 6 или Wave 7. Их актуальность не проверяется ни одним workflow.
Шаги: Добавить в джобу gates шаг self-test reachability на закоммиченном бинарнике и шаг прогона с теми же флагами `-root` и `-min-findings`, что у остальных гейтов. Обновить `.github/workflows/README.md` раздел про ci.yml. Не менять 이름 джобы, таймаут и path-фильтры.
Проверка: CI зелёный. Лог джобы gates содержит строки self-test gate=reachability и gate gate=reachability. Красная проверка при посаженной ошибке не требует ручного запуска гейта локально.
Приёмка: Все пять зарегистрированных гейтов, кроме информационного wiring, имеют обязательный прогон в CI. Устаревшие строки reachability.json вызывают красный результат.

Итог: Cardindex resolves done: OwnerFiles overlap with ATX-C01 (already done). Supporting merged commit d85bf9484ac9 (PR #1597) touches the card OwnerFiles.
Доказательство: Commit d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 (PR #1597, fix(ci): BL-05 run all gate self-tests from committed binary (#1597)) touches OwnerFiles .github/workflows/README.md, .github/workflows/ci.yml; git merge-base --is-ancestor d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1597 d85bf9484ac9
Время закрытия: 2026-11-22T11:27:01Z
