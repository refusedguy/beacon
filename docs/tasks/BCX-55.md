# BCX-55. Грузить реальный плагин репозитория в живом e2e
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: tests/e2e/e2e_test.go, tests/e2e/run.sh, .opencode/plugins/beacon-bridge.js
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Проверять доставку wake через .opencode/plugins/beacon-bridge.js, а не через эталон из fixtures.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 4 и раздел «Живой зонд». opencode_wake_with_reference_plugin использует плагин, скопированный из спайка T0.3. Реальный .opencode/plugins/beacon-bridge.js в живом сервере не грузится. Спецификация docs/evals/e01-e2e-repo-plugin-not-fixture.md не реализована. Базовый замер и приоритет из отчёта: P0.
Шаги: Написать сценарий из e01-e2e-repo-plugin-not-fixture.md. Поднимать сервер с плагином репозитория. Проверить prompt.ok delivery=steer, session.execution.started и запись квитанции от реального плагина. Оставить эталон только как негативный контроль.
Проверка: CI зелёный. Новый сценарий в наборе семи с обновлённым frozenScenarioHash. Живой лог показывает вызовы реального плагина.
Приёмка: Доставка wake подтверждена на том коде, который используют пользователи.
