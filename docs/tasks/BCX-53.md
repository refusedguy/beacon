# BCX-53. Убрать гонку записи квитанции в beacon-bridge.js
Статус: done
Фаза: P0
Тип: task
Владелец файлов: .opencode/plugins/beacon-bridge.js, tests/adapter/receipts-wx.test.js
Зависимости: 
Ссылка: 
Агент: exe32
Время: 2026-11-21T17:37:55Z
Цель: Сделать existsSync признаком полностью записанной строки.
Контекст: Источник: docs/research/e2e-coverage-audit.md, раздел «Три падения» 2 и таблица строка 2. Сессия ses_e02babb9dffeN1QFcKyX5Mqs6E сообщение 768: SyntaxError Unexpected end of JSON input в JSON.parse beacon-bridge.js:959:25. flaky-tests-audit.md:99-116: цикл ждёт только existsSync, затем вызывает stop() и JSON.parse. js-test помечен required: false. Базовый замер и приоритет из отчёта: P0.
Шаги: Писать квитанцию во временный файл с rename или одной writeFileSync полной строки с переводом строки. Перенести JSON.parse в цикл ожидания и вызывать stop() после разбора. Добавить детерминированный тест с 20 повторами. Сделать js-test блокирующим после стабилизации.
Проверка: CI зелёный. Тест с внедрённым планировщиком проходит 20 повторов без SyntaxError. Джоба js-test обязательна.
Приёмка: Чтение квитанции никогда не попадает в середину записи.

## Подтверждение
Исправление гонки квитанции уже лежит в `origin/main` через PR #1214 (карточка ATX-01, статус done). Коммит 85512765a5e4c0e99190e4f8a70e403ee625ff64 меняет цикл ожидания в beacon-bridge.js:946-980: разбор JSON.parse идёт внутри цикла, stop() вызывается после разбора. Проверка `git merge-base --is-ancestor 85512765a5e4c0e99190e4f8a70e403ee625ff64 origin/main` вернула код 0. Дубль кода не создавался.
