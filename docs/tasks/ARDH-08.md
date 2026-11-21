# ARDH-08. Завести один список полей карточки для taskstore и docsgate
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/taskstore/store.go, tools/beacon-gates/docsgate/docs.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
baseline: layers.json содержит 9 разрешённых рёбер, ни одного из tools/beacon-gates в internal/*; корневой go.mod не требует tools/beacon-gates, а tools/beacon-gates/go.mod не требует корневой модуль. Списки из 13 полей живут в requiredFields (store.go) и taskFields (docsgate/docs.go).
Цель: Описать 13 полей карточки один раз и читать этот список и в taskstore, и в docsgate, сохранив явные сообщения об ошибках.
Контекст: Источник: docs/research/go-metaprogramming.md, находка 2 и рекомендация 2, плюс docs/research/duplicate-audit.md, раздел про карточки. Список повторён в store.go:99-102 и docsgate/docs.go:52-59. Риск: низкий. Reflection и теги прячут правила валидации, поэтому словарь должен оставаться читаемым и аудируемым на stdlib.
Шаги: Вынести словарь полей в один экспортируемый список. Использовать его в requiredFields и taskFields. Сохранить порядок полей и текст ошибок ParseCard.
Проверка: Тесты ParseCard и docs gate зелёные в CI. Сгенерированные карточки batch-apply проходят проверку.
Приёмка: Список полей определён один раз, сообщения об ошибках и формат карточки не изменились.

## Подтверждение
Общий список в одном пакете недоступен, поэтому статус остаётся todo. layers.json разрешает импорты только по явному списку рёбер, и ребра tools/beacon-gates в internal/infra в нём нет. Корневой модуль и tools/beacon-gates являются разными модулями. Ни один go.mod не требует другой, поэтому импорт через границу модулей не собирается. Списки полей остались в internal/infra/taskstore/store.go (requiredFields) и tools/beacon-gates/docsgate/docs.go (taskFields). В оба места добавлены взаимные ссылки с точной причиной. Порядок полей и явные сообщения об ошибках ParseCard и docs gate не изменились. PR #1160: https://github.com/refusedguy/beacon/pull/1160.

Повторная проверка 2026-11-21 по свежему origin/main a1efaa89: PR #1160 влит 2026-11-21T13:15:13Z. Взаимные ссылки на месте: internal/infra/taskstore/store.go:99-103 и tools/beacon-gates/docsgate/docs.go:52-59 объясняют, почему списки не объединить. В layers.json tools/beacon-gates указан только как отдельный слой, ребра в internal/* нет. Ни корневой go.mod, ни tools/beacon-gates/go.mod не содержат require друг друга. Общий пакет остаётся недоступным, код и формат карточки не менялись, дублирующих правок нет. Проверки go test, go build и beacon gates запускает CI, локально они не выполнялись.
