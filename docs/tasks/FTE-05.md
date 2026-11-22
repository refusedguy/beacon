# FTE-05. Добавить подкоманду beacon deferred done
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/deferred.go, cmd/beacon/deferred_test.go
Зависимости: FTE-04
Ссылка: docs/templates/wiring-card.md:15,17, docs/templates/wiring-card.md:1285, docs/research/research-feature-gaps.md:15,51
Агент: ses_dfbfdc1faffeRhT1qdIzOWYNKb
Время: 2026-11-22T16:08:32Z
baseline: runDeferred в cmd/beacon/deferred.go:126 разбирает только --format и игнорирует позиционные аргументы. Подкоманды done нет. Метод MarkDeferredDone существует в internal/infra/store/deferred.go:318, из CLI не вызывается.
Цель: Оператор переводит отложенную запись в done из CLI, как требует шаблон wiring-карточки.
Контекст: docs/templates/wiring-card.md:15 предписывает вызвать beacon deferred done после подключения проводки. Строка 17 требует увидеть status: done в выводе beacon deferred. Автоматическое закрытие при влитом PR работает через cmd/beacon/mcp.go, но ручной путь недоступен. Карточки WIRING-485, WIRING-486, WIRING-487 и WIRING-490 повторяют то же требование.
Шаги: Разобрать подкоманду done и аргумент watchID. Вызвать MarkDeferredDone через порт writer. Вернуть ErrDeferredNotFound как код 2. Добавить тест на повторный вызов и неизвестный ID. Обновить описание команды в cmd/beacon/catalog.go:39.
Проверка: beacon deferred done <watchID> возвращает код 0. beacon deferred --format json показывает status: done для этой записи. Повтор возвращает тот же результат без записи. Неизвестный watchID даёт код 2 без изменений в store.
Приёмка: Подкоманда описана в каталоге и покрыта тестом, CI зелёный.

Причина: Уже влито в origin/main: PR #1763 (feat(deferred): add beacon deferred done subcommand), коммит 43da266fc01364cb56dbf1e21b485d749181d89a. git merge-base --is-ancestor 43da266f origin/main -> exit 0. Подкоманда done и тесты уже в cmd/beacon/deferred.go и cmd/beacon/deferred_test.go (TestDeferredDoneCLIExitCodes, TestDeferredDoneDispatchJSON). Дубль кода не создаём.
Закрыл: ses_dfbfdc1faffeRhT1qdIzOWYNKb
Время закрытия: 2026-11-22T16:08:40Z