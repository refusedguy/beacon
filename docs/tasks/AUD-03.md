# AUD-03. Сохранять stderr в CLI-фолбэке и распознавать 404/409
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/wake/deliver.go, internal/infra/wake/deliver_test.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Не терять stderr в CLI-фолбэке и распознавать ошибки 404/409 как sessionNotFound.
Контекст: Источник: аудит fix/c18-a17, P0.2. OpenCodeDeliverer.deliverCLI и T202API.run в internal/infra/wake/deliver.go вызывают exec.CommandContext(...).Output(), из-за чего stderr теряется. sessionNotFound не распознаёт 404/409 из stderr. baseline: 0 чтений stderr в этих двух путях.
Шаги: Заменить Output() на CombinedOutput() в deliverCLI и T202API.run. Добавить разбор stderr на 404/409 в sessionNotFound. Обновить тесты на случай с ошибкой в stderr и на коды 404/409.
Проверка: CI зелёный. Тест подтверждает, что stderr попадает в out и что 404/409 из stderr трактуются как sessionNotFound.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.
