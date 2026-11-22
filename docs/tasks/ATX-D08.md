# ATX-D08. Подтвердить устаревшую premise карточек про FACTS
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, docs/research/docs-consistency-gaps.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Зафиксировать, что генератор FACTS уже исправлен, и не писать дубль кода.
Контекст: Источник: docs/research/docs-consistency-gaps.md, находка ATX-D08. `docs/FACTS.md` отсутствует в git и не пишется генератором после PR 929 (IDX-11). Функция `factsLink` в `internal/infra/batchapply/apply.go` уже добавляет `../` для владельцев вне каталога `docs`, то есть правка из BCX-08 в коде есть. Карточка BCX-08 остаётся в todo, HD-088 уже done. Статусы карточек в этом PR не менялись.
Шаги: Добавить в таблицу отчёта строку с доказательством по BCX-08: путь функции, номер PR и SHA. Оставить в генераторе self-test разрешения ссылок для владельцев внутри и вне `docs/`. Передать вопрос о статусе BCX-08 владельцу карточки через индекс.
Проверка: CI зелёный. Self-test `factsLink` проходит для owner внутри `docs`, корневого `README.md` и `cmd/beacon/observe.go`. В отчёте есть SHA и номер PR.
Приёмка: Дубль кода в генераторе не появился. Чужие карточки не изменены. Решение о BCX-08 зафиксировано владельцем.

Итог: Cardindex resolves done: OwnerFiles overlap with ATX-12 (already done). Supporting merged commit 7657fed34087 (PR #1511) touches the card OwnerFiles.
Доказательство: Commit 7657fed34087e26f6fe42d629ed54afef0bae4b1 (PR #1511, fix(batchapply): ST5-02 принять статусы taskstore в batch-apply (#1511)) touches OwnerFiles docs/research/docs-consistency-gaps.md, internal/infra/batchapply/apply.go; git merge-base --is-ancestor 7657fed34087e26f6fe42d629ed54afef0bae4b1 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1511 7657fed34087
Время закрытия: 2026-11-22T11:27:01Z
