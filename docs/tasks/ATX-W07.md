# ATX-W07. Защитить закрытие Watch от устаревшей ревизии
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/orchestrator_store.go, internal/app/orchestrator/orchestrator.go, cmd/beacon/orchestrator_store_test.go
Зависимости: 
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W07
Агент: нет
Время: нет
Цель: Не дать позднему подтверждению мержа перезаписывать закрытие Watch по смене head.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Закрытие без проверки ревизии». `confirmMerge` в `internal/app/orchestrator/orchestrator.go:549-565` вызывает `st.CloseWatch(ctx, w.ID)` без ревизии. Реализация в `cmd/beacon/orchestrator_store.go:119-139` читает Watch, проверяет только статус и пишет `closed`, `merged`, `merged` поверх всей записи. Если мост уже закрыл Watch с причиной `head_changed`, поздний merge меняет причину и вердикт. Baseline: `RecordMerge` использует ревизию, а `CloseWatch` её не знает.
Шаги: Расширить порт `CloseWatch` ревизией и ожидаемым `HeadSHA`. В реализации сделать условную запись, которая меняет поля только при совпадении ревизии и ожидаемого `HeadSHA`. При расхождении возвращать типизированный конфликт и не сохранять запись. Обновить вызов в `confirmMerge` и фейковые store в тестах. Добавить тест с параллельным закрытием по head и по merge.
Проверка: CI зелёный. Причина закрытия остаётся `head_changed`, вторая запись не перезаписывает первую. Обычный merge по актуальной ревизии по-прежнему закрывает Watch.
Приёмка: Закрытие Watch имеет одну причину и не зависит от порядка двух гонящих путей.

Причина: Already landed in PR #1653 (commit 7e1c96a13b85c071d415af1814c00fbcdbe03bde): guard watch close with revision and head check (ATX-W07). git merge-base --is-ancestor 7e1c96a13 origin/main exit 0.
Закрыл: ses_dfcd81a70ffe6MNjcwhRE5cexF
Время закрытия: 2026-11-22T12:09:10Z
