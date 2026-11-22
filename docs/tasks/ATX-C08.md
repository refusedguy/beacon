# ATX-C08. Обновить документацию workflow после PR 1235, 1237, 1239 и 1259
Статус: done
Фаза: P3
Тип: task
Владелец файлов: .github/workflows/README.md, .github/workflows/ci.yml
Зависимости: ATX-C01
Ссылка: docs/research/ci-gates-gaps.md, находка C08; PR #1235, #1237, #1239, #1259
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Привести описание workflow и комментарии в ci.yml к текущему состоянию.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C08. Замер: `.github/workflows/README.md:5` описывает `actions/cache` для build, test и gates, хотя #1235 и #1237 заменили его на композитный экшен. README:20 сообщает о пяти джобах ci.yml и e2e внутри основного workflow, однако сейчас в ci.yml восемь джоб, а e2e вынесен в отдельный файл. README:43 указывает лимит e2e 20 минут при фактических 5 минутах в `e2e.yml:38`. `.github/workflows/ci.yml:198-200` называет docs-гейт информационным, хотя #1259 сделал его обязательным.
Шаги: Переписать разделы README про подготовку Go, состав джоб и лимит e2e. Добавить список обязательных чеков из ATX-C01 и пометить e2e и ci-provider-probe как информационные. Исправить комментарий над wiring-гейтом в ci.yml. Оставить структуру workflow и коды выхода без изменений.
Проверка: CI зелёный. `rg -n '20 минут|пяти независимых|actions/cache' .github/workflows/README.md` даёт 0 строк. Список чеков в README совпадает с ответом GitHub API из ATX-C01.
Приёмка: Документация описывает текущие триггеры, джобы и статус проверок. Устаревших утверждений о кэше и лимитах нет.

Итог: Cardindex resolves done: OwnerFiles overlap with ATX-C01 (already done). Supporting merged commit d85bf9484ac9 (PR #1597) touches the card OwnerFiles.
Доказательство: Commit d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 (PR #1597, fix(ci): BL-05 run all gate self-tests from committed binary (#1597)) touches OwnerFiles .github/workflows/README.md, .github/workflows/ci.yml; git merge-base --is-ancestor d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1597 d85bf9484ac9
Время закрытия: 2026-11-22T11:27:01Z
