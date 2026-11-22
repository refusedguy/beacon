# BCX-79. Исправить состояние OTEL_EXPORTER_OTLP_PROTOCOL=grpc
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/observe/otel.go, docs/tasks/T3.393.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать обещание экспорта, который код отвергает.
Контекст: Источник: docs/research/config-env-audit.md, итог строка 9. observe/otel.go:38-39 называет grpc допустимым значением. normalizeProtocol (:40) принимает его, но NewOTLPExporter (:367-369) отвергает и включает local fallback. Базовый замер и приоритет из отчёта: P2.
Шаги: Либо реализовать grpc-экспорт, либо убрать grpc из списка допустимых значений и документации. Добавить тест, что объявленное значение приводит к реальному экспорту или к явной ошибке, а не к тихому fallback.
Проверка: CI зелёный. Тест на grpc даёт ожидаемый результат. Документация и код перечисляют одинаковые протоколы.
Приёмка: Документация не обещает работающий экспорт, который молча отключается.

Итог: Already landed in PR #1343 (commit 88a830d99fb04cf101372322cebaf1416a06afc0). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1343 merged 2026-11-22T09:28:59+05:00; commit 88a830d99fb04cf101372322cebaf1416a06afc0; git merge-base --is-ancestor 88a830d99fb04cf101372322cebaf1416a06afc0 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1343 88a830d9
Время закрытия: 2026-11-22T11:24:28Z
