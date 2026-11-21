# ARDH-05. Перенести checkFixture в пакет gate
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: tools/beacon-gates/gate/, tools/beacon-gates/capabilities/selftest.go, tools/beacon-gates/layers-ast/selftest.go, tools/beacon-gates/layers-list/selftest.go, tools/beacon-gates/reachability/selftest.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить четыре копии checkFixture функцией gate.RunFixtures, принимающей список файлов, функцию analyse и функцию очистки.
Контекст: Источник: docs/research/duplicate-audit.md, дубль 7. Тела почти совпадают, отличаются вызов analyse и способ удаления fixture. Выгода около 60 строк. Риск: низкий. После правки надо пересобрать закоммиченный бинарник гейтов, как описано в .github/workflows/README.md.
Шаги: Добавить RunFixtures в пакет gate с тестом. Перевести четыре self-test. Пересобрать tools/beacon-gates/beacon-gates и закоммитить его в том же PR.
Проверка: Self-test каждого гейта проходит в CI, проверка совпадения бинарника зелёная.
Приёмка: Четыре копии checkFixture удалены, правила гейтов и их ожидания не изменились.
