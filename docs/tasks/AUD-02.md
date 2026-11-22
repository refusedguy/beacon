# AUD-02. Передавать delivery и resume в Prompt вместо потери поля
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/opencode/client.go, internal/infra/opencode/client_test.go, cmd/beacon/daemon_wake_adapter.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Передавать delivery и resume в Prompt и перевести daemon-адаптер на HTTP-порт.
Контекст: Источник: аудит fix/c18-a17, P0.2. opencodeWakePorts в cmd/beacon/mcp.go передаёт delivery в prompt, но Client.Prompt в internal/infra/opencode/client.go шлёт только {"text": text}. daemonWakeAdapter в cmd/beacon/daemon_wake_adapter.go использует wake.T202API (CLI-подпроцесс) вместо HTTP-клиента. baseline: 0 полей delivery/resume в теле запроса.
Шаги: Сверить схему Prompt с локальным opencode API по openapi.json или контракту адаптера и записать источник. Добавить передачу delivery, resume и идемпотентного ID в Client.Prompt, если они предусмотрены контрактом. Перевести daemonWakeAdapter на общий HTTP-порт openCodePort с честным fallback на CLI. Обновить тесты клиента на тело запроса.
Проверка: CI зелёный. Тест клиента проверяет, что delivery и resume попадают в JSON запроса. daemonWakeAdapter использует HTTP-порт и откатывается на CLI только по описанному условию.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.
