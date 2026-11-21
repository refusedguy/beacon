# ARDH-05. Перенести checkFixture в пакет gate
baseline: до правки четыре копии checkFixture по 19 строк в tools/beacon-gates/{capabilities,layers-ast,layers-list,reachability}/selftest.go, аудит docs/research/duplicate-audit.md (дубль 7) оценивал выгоду около 60 строк. Замер снят до изменений.
Статус: done
Фаза: P2
Тип: task
Владелец файлов: tools/beacon-gates/gate/, tools/beacon-gates/capabilities/selftest.go, tools/beacon-gates/layers-ast/selftest.go, tools/beacon-gates/layers-list/selftest.go, tools/beacon-gates/reachability/selftest.go
Зависимости: 
Ссылка: https://github.com/refusedguy/beacon/pull/1162
Агент: нет
Время: нет
Цель: Заменить четыре копии checkFixture функцией gate.RunFixtures, принимающей список файлов, функцию analyse и функцию очистки.
Контекст: Источник: docs/research/duplicate-audit.md, дубль 7. Тела почти совпадают, отличаются вызов analyse и способ удаления fixture. Выгода около 60 строк. Риск: низкий. После правки надо пересобрать закоммиченный бинарник гейтов, как описано в .github/workflows/README.md.
Шаги: Добавить RunFixtures в пакет gate с тестом. Перевести четыре self-test. Пересобрать tools/beacon-gates/beacon-gates и закоммитить его в том же PR.
Проверка: Self-test каждого гейта проходит в CI, проверка совпадения бинарника зелёная.
Приёмка: Четыре копии checkFixture удалены, правила гейтов и их ожидания не изменились.

## Подтверждение
- Реализация в PR #1162. Четыре копии checkFixture удалены. Self-test'ы передают список gate.FixtureCase, функцию analyse и cleanup в gate.RunFixtures. capabilities адаптирует вызов analyse(root, gate.Exceptions{}) обёрткой. Правила гейтов и ожидания не изменились.
- Добавлен gate/runfixtures_test.go с проверками прохода всех кейсов, невыполненного Want и ошибки analyse.
- Закоммиченный tools/beacon-gates/beacon-gates пересобран с Go 1.26.0 и флагами CI. Локально с новым бинарником прошли self-test'ы capabilities (3 fixture), layers-ast (5), layers-list (4), reachability (5), код выхода 0. Полные проверки запускает CI.
