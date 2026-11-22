# BCX-56. Добавить живой тест dedup после рестарта
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tests/chaos/duplicate-delivery_test.go, evals/main.go, docs/evals/rs-wake-dedup-across-restart.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Проверить, что второй steer после рестарта не уходит.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 5 и раздел «Живой зонд». Chaos на реальном store проверяет двойную доставку одним эффектом, но лестница и эскалация там фейки (duplicate-delivery_test.go:30-33, :122-124). eval 18_w09_receipt_outcomes работает на фейковом хосте. rs-wake-dedup-across-restart.md остаётся текстом. Базовый замер и приоритет из отчёта: P1.
Шаги: Построить сценарий с реальным указателем сессии: доставить steer, перезапустить процесс, повторить событие. Утверждать один вызов session.prompt и одну квитанцию. Не использовать фейковый хост как доказательство.
Проверка: CI зелёный. Живой сценарий регистрирует ровно один steer после рестарта. Негативный контроль показывает дубль без защиты.
Приёмка: Идемпотентность доставки подтверждена на реальном store и живом адаптере.

Итог: Already landed in PR #1337 (commit 151fa9877cbec70ccac53f0333a80e0baac188b9). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1337 merged 2026-11-22T09:06:16+05:00; commit 151fa9877cbec70ccac53f0333a80e0baac188b9; git merge-base --is-ancestor 151fa9877cbec70ccac53f0333a80e0baac188b9 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1337 151fa987
Время закрытия: 2026-11-22T11:24:28Z
