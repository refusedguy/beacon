# AUTOSP-11. Памятный гейт перед запуском раннера
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/server/metrics.go, internal/infra/server/metrics_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Не запускать нового агента при превышении порога RSS.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 11. internal/infra/server/metrics.go:406 FootprintCheck публикует rss_bytes и возвращает решение по порогу. Автоспавнер этого гейта не вызывает. baseline: лимит слотов 4 по internal/domain/orchestrator.go:17 ограничивает число воркеров, но не их суммарную память.
Шаги: Подготовить метод, который возвращает решение о запуске на основе FootprintCheck и текущего числа Watch. Добавить тест metrics_test.go на порог и на отсутствие readRSS. Не менять формат метрик.
Проверка: CI зелёный. При превышении порога метод возвращает отказ. При недоступном RSS решение консервативно.
Приёмка: Автоспавнер может вызвать гейт до старта раннера и удержать слот.

Итог: Already landed in #1437 (95843cb669ef62ac7214dc3ff01f9bb84e0818e5). No duplicate code needed.
Доказательство: PR #1437 commit 95843cb669ef62ac7214dc3ff01f9bb84e0818e5 merge-base --is-ancestor 95843cb669ef62ac7214dc3ff01f9bb84e0818e5 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1437 95843cb
Время закрытия: 2026-11-22T11:29:08Z
