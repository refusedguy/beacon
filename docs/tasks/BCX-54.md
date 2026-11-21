# BCX-54. Реализовать живой прогон seeded regression artery
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: tests/scenarios/seeded.yaml, internal/testsystem/harness.go, .github/workflows/ci.yml, docs/tasks/S7.011.md
Зависимости: 
Ссылка: ADR 0214
Агент: нет
Время: нет
Цель: Заменить grep-проверку спецификации настоящим прогоном healthy, seeded, healthy_again.
Контекст: Источник: docs/research/e2e-coverage-audit.md, раздел «Три падения» 3 и таблица строка 3. docs/evals/w25-seed-regression-artery.md требует трёх прогонов. ADR 0214 не считает покрытие одними impl-тестами. Коммит 63cffc79 (PR #1145) добавил seeded.yaml и internal/testsystem, но CI-шаг seed-spec в ak2-run2.log:194-208 только grep-проверяет имена и запускает три модельных теста. Fault controller нет. Базовый замер и приоритет из отчёта: P0.
Шаги: Реализовать fault controller для четырёх отказов из S7.011: lost baton, late event, pruned cursor, duplicate clientRequestId. Запускать три прогона в одной джобе и требоватьhealthy pass, seeded fail на ожидаемом маркере, healthy_again pass. Сохранить негативный контроль. Не принимать grep-проверку как замену.
Проверка: CI зелёный. Лог джобы показывает три прогона с ожидаемыми исходами. Без seed диагностического значения нет.
Приёмка: Пара сценариев по ADR 0214 выполнена живым прогоном, а не проверкой текста спецификации.
