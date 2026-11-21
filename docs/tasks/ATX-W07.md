# ATX-W07. Защитить закрытие Watch от устаревшей ревизии
Статус: todo
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
