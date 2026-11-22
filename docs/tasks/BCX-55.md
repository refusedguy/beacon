# BCX-55. Грузить реальный плагин репозитория в живом e2e
Статус: done
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

Итог: Already landed in PR #1324 (commit 397d87bda6306c633d7d2505a4ca4e97d0ef99f7). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1324 merged 2026-11-21T22:57:46+05:00; commit 397d87bda6306c633d7d2505a4ca4e97d0ef99f7; git merge-base --is-ancestor 397d87bda6306c633d7d2505a4ca4e97d0ef99f7 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1324 397d87bd
Время закрытия: 2026-11-22T11:24:28Z
