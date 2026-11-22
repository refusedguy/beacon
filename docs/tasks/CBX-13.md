# CBX-13. Забронированные ID остаются мёртвыми для create, import и rename

Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/taskstore/store.go, internal/infra/taskstore/store_test.go
Зависимости: CBX-17
Ссылка: https://github.com/gastownhall/beads (коммит 78624109904655d7a4609f27d34eb0982cd8963f)
Агент: нет
Время: нет
Baseline: beads: internal/storage/issueops/id_retention.go:14-23, две таблицы tombstones по одной на область хранения; в beacon CreateExclusive в internal/infra/taskstore/store.go:796-815 проверяет только существование файла, а удаление карточки не оставляет следа.

Цель: Не оживлять однажды выведенный из обращения ID через любую из трёх путей записи.

Контекст: Источник, реальный код beads. IDTombstoneTable и WispIDTombstoneTable в internal/storage/issueops/id_retention.go:14-30 хранят ID и closed_at; версионированная таблица пишется как issues, локальная - как wisps. idOccupancySQL на :39-46 опрашивает четыре места одним выражением: две таблицы строк и две таблицы могил, один snapshot на любом isolation level. probeIDOccupancyInTx на :48-84 отдаёт приоритет живой строке перед надгробием. CreateOnlyGuard в internal/storage/issueops/create_only_guard.go:15-51 добавляет shard-координацию :53-95, чтобы create и sweep не гонялись за одним ID. rename_prefix.go:400-426 и :653-664 запрещают переименование на забронированный ID и на любое место назначения семьи. Разрыв в beacon: CreateExclusive в internal/infra/taskstore/store.go:796-815 использует O_EXCL и не знает об удалённых ID; MeasureIDCollisions на :726-794 только считает коллизии между worktree.

Шаги: Ввести журнал забронированных ID в state dir с временем закрытия. Проверять его в create, в импорте и при переименовании. Делать проверку одним чтением и отдавать приоритет живой карточке. Координировать бронирование и создание通过 тем же замком, что и запись карточки. Добавить typed-ошибку с истёкшей идемпотентностью.

Проверка: Измеримая. После бронирования ID T3.900 create, import и rename на этот ID возвращают одну и ту же ошибку, число файлов в docs/tasks не меняется. Если живая карточка T3.900 существует вместе с записью журнала, create получает ошибку 'ID занят', а не 'истёк'. Двадцать параллельных create одного ID дают ровно одну запись и девятнадцать отказов.

Приёмка: CB-02 владеет разрешением коллизий, эта карточка только блокировкой повторного использования. Формат существующих ID не меняется.
