# AUD-08. Не принимать не-T карточки за пути файлов git
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/app/orchestrator/store.go, internal/app/orchestrator/store_test.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Проверять taskstore.IsID перед трактовкой зависимости как git-пути.
Контекст: Источник: аудит fix/c18-a17, P1.5. dependencyCardRef в internal/app/orchestrator/store.go на t-цифры принимает любые не-T ID (ARDH-08, ATX-R01, BS.1, S1.001) как git-путь. baseline: 0 проверок taskstore.IsID перед трактовкой как путь.
Шаги: Перед трактовкой строки как git-пути проверять taskstore.IsID. Если строка похожа на ID карточки, но невалидна, отвергать её явно. Добавить тесты на ARDH-08, ATX-R01, BS.1, S1.001 и на настоящий путь файла.
Проверка: CI зелёный. Тесты подтверждают, что не-T карточки не становятся путями git и что настоящие пути проходят.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.
