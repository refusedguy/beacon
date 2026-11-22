# BCX-58. Добавить живой тест registry с двумя проектами
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/registry/, .github/workflows/ci.yml, cmd/beacon/registry_sync.go
Зависимости: 
Ссылка: ADR 0086
Агент: нет
Время: нет
Цель: Проверить границу проекта на реальных сессиях, а только на моделях и конфиге.
Контекст: Источник: docs/research/e2e-coverage-audit.md, таблица строка 7. Есть unit-тесты registry, модельные eval OW02 и OW03, джоба registry-check на origin/main проверяет конфиг позитивом и негативом. Живого теста с двумя проектами в одном сервере и реальными сессиями нет. Базовый замер и приоритет из отчёта: P1.
Шаги: Поднять сервер с двумя проектами и разными watchID. Проверить, что реестр разделяет сессии и что sync не вызывает wake по stale. Добавить негатив с пересекающимися именами агентов.
Проверка: CI зелёный. Живой сценарий с двумя проектами проходит. Негатив с stale не инициирует wake.
Приёмка: Граница проекта подтверждена на реальных сессиях, а не только на конфиге.

Итог: Already landed in PR #1336 (commit b990b5180842edb282ceb3b0bd365e014449da01). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1336 merged 2026-11-22T09:30:20+05:00; commit b990b5180842edb282ceb3b0bd365e014449da01; git merge-base --is-ancestor b990b5180842edb282ceb3b0bd365e014449da01 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1336 b990b518
Время закрытия: 2026-11-22T11:24:28Z
