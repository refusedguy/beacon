# ATX-W06. Устранить гонку закрытия head с регистрацией Watch
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/cibridge/verdict_applier.go, internal/infra/cibridge/ci_refresh.go, internal/domain/fanout.go, internal/infra/cibridge/bridge_test.go
Зависимости: 
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W06
Агент: нет
Время: нет
Цель: Гарантировать один активный Watch на алиас repo, PR, head при смене head и параллельной регистрации.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Гонка закрытия head». `handleHeadChanged` в `internal/infra/cibridge/verdict_applier.go:18-81` сначала закрывает старый Watch, затем проверяет ID преемника и сохраняет его. ID из `successorWatchID` в `ci_refresh.go:157-161` не имеет суфикса выпуска, а `fanout.watchID` в `internal/domain/fanout.go:248-257` его имеет. Регистрация в окне между двумя записями создаёт второй Watch на новый SHA. Итог: два набора обязательств и две точки доставки. Baseline: предыдущий аудит описывал похожую гонку для blocked Watch, исправление затронуло только поиск алиаса в `Fanout`, не саму транзакцию преемника.
Шаги: Свести оба пути к одному детерминированному IDWatch для алиаса. Вынести закрытие старого Watch и создание преемника в одну транзакцию store с проверкой уникальности активного алиаса. При существующем преемнике объединять подписчиков и обязательства вместо выхода с `head_successor_exists`. Добавить тест с паузой в фейковом store после первого сохранения.
Проверка: CI зелёный. После гонки ровно один Watch в статусе watching на новый SHA и один набор обязательств. Старый Watch закрыт с причиной `head_changed`.
Приёмка: Повторная регистрация на новый head не создаёт дубль и не теряет подписчиков.

Итог: Cardindex resolves done: OwnerFiles overlap with AUTOSP-01 (already done). Supporting merged commit 059bbcd4ad85 (PR #1619) touches the card OwnerFiles.
Доказательство: Commit 059bbcd4ad85ca3590df60b8425ea31e914d3477 (PR #1619, fix(cibridge): persist checks and merge state on watching verdict (#1619)) touches OwnerFiles internal/domain/fanout.go, internal/infra/cibridge/bridge_test.go, internal/infra/cibridge/ci_refresh.go, internal/infra/cibridge/verdict_applier.go; git merge-base --is-ancestor 059bbcd4ad85ca3590df60b8425ea31e914d3477 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1619 059bbcd4ad85
Время закрытия: 2026-11-22T11:28:03Z
