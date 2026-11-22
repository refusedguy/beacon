# ATX-W04. Остановить рост обязательств у orphaned подписчиков
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/wake_dispatcher.go, internal/infra/cibridge/bridge.go, internal/infra/cibridge/wake_deferred_test.go
Зависимости: 
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W04
Агент: нет
Время: нет
Цель: Не создавать новые обязательства для подписчика в фазе `orphaned` и помечать его старые обязательства завершёнными.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Обязательства orphaned подписчика». `Bridge.wakeOne` в `internal/infra/cibridge/bridge.go:354-364` пропускает wake для фазы `orphaned`, но `Fanout.ensureObligation` продолжает создавать строку на каждую регистрацию и каждую ревизию. `markOrphaned` в `wake_dispatcher.go:593-660` меняет фазу и шлёт синтетическое обязательство `orphan:<watchID>`, но не трогает обязательства самого подписчика. GC из PR #1275 защищает обязательства живых Watch. Baseline: живой Watch с мёртвым исполнителем копит по одной строке на ревизию без верхней границы.
Шаги: Перед созданием обязательства в `Fanout` или в точке доставки проверять фазу подписчика. При переводе в `orphaned` в той же транзакции помечать открытые обязательства завершённым состоянием или удалять их через `EraseObligation` с отдельной причиной. Сохранить синтетическое обязательство оркестратору для эскалации. Добавить тест с живым Watch, двумя ревизиями и подписчиком в фазе `orphaned`.
Проверка: CI зелёный. Число строк обязательств подписчика не растёт после двух новых вердиктов. Эскалация оркестратору приходит один раз.
Приёмка: У мёртвого исполнителя нет висящих обязательств, живой Watch не накапливает мусор в таблице.

Итог: Cardindex resolves done: OwnerFiles overlap with ATX-19 (already done). Supporting merged commit 4653592ac51c (PR #1535) touches the card OwnerFiles.
Доказательство: Commit 4653592ac51cf4c5a96db59f328eac1875604107 (PR #1535, feat(cibridge): publish unlock events after merged card close (AN-02) (#1535)) touches OwnerFiles internal/infra/cibridge/bridge.go, internal/infra/cibridge/wake_deferred_test.go, internal/infra/cibridge/wake_dispatcher.go; git merge-base --is-ancestor 4653592ac51cf4c5a96db59f328eac1875604107 origin/main exit 0; duplicate resolved by cardindex rules.
Причина: already landed in PR #1535 4653592ac51c
Время закрытия: 2026-11-22T11:27:01Z
