# ATX-C03. Включить кэш Go в docs workflow
Статус: done
Фаза: P2
Тип: task
Владелец файлов: .github/workflows/docs.yml
Зависимости: 
Ссылка: docs/research/ci-gates-gaps.md, находка C03; PR #1235
Агент: нет
Время: 2026-11-21T17:42:57Z
Цель: Убрать последнее переопределение, которое отключает кэш Go в workflow docs.
Контекст: Источник: docs/research/ci-gates-gaps.md, находка C03. Замер из `docs/research/ci-speed-audit.md:21`: джоба docs занимает около 21 с, из них setup-go 5 с и 8 с по оценке в строке 88. `.github/workflows/docs.yml:28-30` передаёт `cache: 'false'` в композитный экшен. В `gates-binary.yml:28` стоит `cache: 'true'`, остальные джобы используют значение по умолчанию.
Шаги: Удалить блок `with:` с `cache: 'false'` из шага Set up Go. Оставить вызов `.github/actions/go-runner-setup`, path-фильтры, self-test и обязательный статус джобы без изменений.
Проверка: CI зелёный. `rg -n "cache: 'false'" .github/workflows/` находит только упоминания в комментариях, если они есть. Лог джобы docs показывает восстановление кэша setup-go.
Приёмка: Все workflow используют общий кэш Go. Проверки docs и их коды выхода не изменились.

Итог: Cardindex resolves done: OwnerFiles overlap with ATX-26 (already done). Supporting merged commit d85bf9484ac9 (PR #1597) touches the card OwnerFiles.
Доказательство: Commit d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 (PR #1597, fix(ci): BL-05 run all gate self-tests from committed binary (#1597)) touches OwnerFiles .github/workflows/docs.yml; git merge-base --is-ancestor d85bf9484ac9bd29fe783fe863fc2ed2d65e29f8 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1597 d85bf9484ac9
Время закрытия: 2026-11-22T11:27:01Z
