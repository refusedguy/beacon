# CLEAN-02. Удалить пять неимпортируемых серверных пакетов
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/server/http/, internal/infra/server/eventlog/, internal/infra/server/eventbus/, internal/infra/server/recovery/, internal/infra/server/store/
Зависимости: 
Ссылка: ADR 0086, ADR 0094
Агент: нет
Время: нет
Цель: Удалить пять подпакетов internal/infra/server, на которые нет ссылок за пределами собственных тестов.
Контекст: Источник: docs/research/cleanup-plan.md, задача 3, и docs/research/deadcode-audit.md, строки про eventbus, eventlog, http, recovery и store. Около 1005 строк кода и 1661 строка тестов. Внешних импортёров нет. Карточки S1.003, S1.006, S1.007, S1.008, S2.001, S2.007 и S7.001 помечены dropped с формулировкой, что дизайн брошен. Риск: средний по смыслу, низкий по технике. Удаление отменяет реализации из PR 508-520.
Шаги: Проверить импорты по явным каталогам internal, cmd, tools, tests. Удалить пять каталогов вместе с тестами. Проверить, что пакет internal/infra/server как точка входа остался. Не трогать живые internal/infra/httpapi и internal/infra/store.
Проверка: CI на PR показывает зелёные build и test. Поиск по internal, cmd, tools не находит импортов удалённых путей.
Приёмка: Пять каталогов удалены, живой HTTP и store не изменены, required-гейты не требуют правки конфигов.
