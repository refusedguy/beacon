# CBX-07. Две половины защиты от устаревших данных при импорте: prefilter и условный upsert

Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, internal/infra/batchapply/apply_test.go
Зависимости: 
Ссылка: https://github.com/gastownhall/beads (коммит 78624109904655d7a4609f27d34eb0982cd8963f)
Агент: ses_cbx07_executor
Время: 2026-11-22T17:59:42Z
Baseline: beads: cmd/bd/import_shared.go:156-161, prefilter сам по себе пропускает гонку; вторая половина проверяет updated_at внутри самого upsert. В beacon internal/infra/batchapply/apply.go:96-116 не различает свежесть входных и локальных артефактов.

Цель: Не перезаписывать свежую локальную карточку более старой входной строкой, даже если локальная правка пришла между чтением и записью.

Контекст: Источник, реальный код beads. importIssuesCore на cmd/bd/import_shared.go:149-180 сначала filterStaleImportIssues, который сообщает StaleSkippedIDs и держит aux-строки вне пачки (:156-174). Отдельное чтение создаёт окно: локальная правка, зафиксированная между ним и batch write, проскочит, поэтому вторая половина RejectStaleUpserts повторно проверяет updated_at внутри upsert (:179-180). ImportOptions.AllowStale на :68-73 - единственный явный путь перезаписи, и он недоступен автоимпорту. Разрыв в beacon: internal/infra/batchapply/apply.go:96-116 содержит только флаги DryRun, Check, MergePlan и Commit; atomicWrite в internal/infra/taskstore/store.go:817-864 публикует файл без сравнения версий, ReadCounter/WriteCounter на :545-575 не хранят времени изменения артефакта.

Шаги: Добавить время изменения в метаданные каждого артефакта. Перед пачкой отфильтровать строки старше локальных и вернуть их список. Повторить проверку непосредственно перед публикацией каждого файла под тем же замком. Добавить явный флаг принудительной перезаписи только для ручного вызова. Сделать обе проверки одним источником правила.

Проверка: Измеримая. Локальная карточка изменена между prefilter и записью: запись отклонена, StaleSkippedIDs содержит ровно один ID, локальный текст побайтово прежний. При том же сценарии с флагом принудительной перезаписи файл изменён и отчёт содержит одно поле UpdatedIssues. Десять повторов дают десять отказов без частично записанных файлов.

Приёмка: CBX-06 задаёт куски импорта, CBX-08 задаёт форму отчёта. Dry-run не пишет файлы.

Причина: Already landed in PR #1800, squash commit ab01923b863b805d278974e93d7eb6618f60cac6. Verified git merge-base --is-ancestor ab01923b origin/main exit 0. OwnerFiles internal/infra/batchapply/apply.go and apply_test.go already carry the stale-row prefilter, in-upsert updated_at recheck and force override in main. No duplicate code written.
Закрыл: ses_cbx07_executor
Время закрытия: 2026-11-22T17:59:51Z