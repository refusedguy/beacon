# CBX-08. Машиночитаемый отчёт импорта о перезаписанных локальных строках и равных метках времени

Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, internal/infra/batchapply/apply_test.go
Зависимости: CBX-07
Ссылка: https://github.com/gastownhall/beads (коммит 78624109904655d7a4609f27d34eb0982cd8963f)
Агент: ses_cbx08c_executor
Время: 2026-11-22T18:02:43Z
Baseline: beads: cmd/bd/import_shared.go:92-106, три отдельных списка UpdatedIssues, TieKeptLocalIDs и StaleSkippedIDs; в beacon форматы отчёта в internal/infra/batchapply/apply.go:68-85 не различают созданное, обновлённое и отклонённое по времени.

Цель: Делать видимым каждое изменение локального состояния, которое импорт всё же внёс, вместо тихой подмены.

Контекст: Источник, реальный код beads. ImportResult на cmd/bd/import_shared.go:76-107 считает Created, Updated, Unchanged, Skipped, Deleted, Collisions и отдельно StaleSkippedIDs, SkippedDependencies, UpdatedIssues (:92-96) и TieKeptLocalIDs (:97-101). UpdatedIssues несёт покадровое поле Changes (:109-113), чтобы revert локального состояния был виден. TieKeptLocalIDs фиксирует случай равных меток времени и разного содержимого: upsert оставляет локальную строку, но auxiliary-данные всё равно сливаются (:97-101). RetiredIDs на :102-106 отделяет ID, которые sweep вывел из обращения. Разрыв в beacon: internal/infra/batchapply/apply.go:68-94 задаёт только checkReportFormat, summaryReportFormat, hashReportFormat и строковые предупреждения; нет ни structured-полей, ни различения равных меток.

Шаги: Добавить в результат batchapply списки созданных, обновлённых, отклонённых как устаревшие и сохранённых по равенству меток. Для обновлённых писать поле changes с именами изменённых полей. Для равных меток-fixtures сохранять локальный текст и сливать только дополнительные данные. Выводить те же списки в JSON и в текстовом отчёте. Не смешивать с пирамидой из CB-02: здесь нет карты соответствий ID.

Проверка: Измеримая. Импорт трёх строк: новая, более старая и с равной меткой но другим текстом. Отчёт: Created равно 1, StaleSkippedIDs содержит один ID, TieKeptLocalIDs содержит один ID, UpdatedIssues пуст. Повторный импорт тех же трёх строк оставляет Created равным нулю и не меняет файлы. JSON отчёт интерпретируется без парсинга текста.

Приёмка: CB-02 владеет IDMapping и CollisionIDs; эта карточка не добавляет переименований. Формат существующих текстовых строк сохраняется.

Причина: Реализация влита в origin/main коммитом 95fac608afe9b25534bc2a0dbbb77383df768f07 через PR #1837 (feat(batchapply): machine-readable import report with tie handling). Проверка git merge-base --is-ancestor 95fac608afe9b25534bc2a0dbbb77383df768f07 origin/main вернула exit 0. PR #1837 изменил ровно OwnerFiles: internal/infra/batchapply/apply.go и apply_test.go. В main присутствуют CreatedIDs, UpdatedIDs, StaleSkippedIDs, TieKeptLocalIDs, WrittenPaths, Changes и обработка равных меток. Дубль кода не создаём.
Закрыл: ses_cbx08c_executor
Время закрытия: 2026-11-22T18:03:39Z