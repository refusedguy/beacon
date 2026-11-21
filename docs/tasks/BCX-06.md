# BCX-06. Переименовать 66 файлов ADR с именем untitled
Статус: done
Фаза: P1
Тип: task
Владелец файлов: docs/adrs/0240-rule-check-without-full-review.md, docs/adrs/0090-honest-delivery-unchanged-ladder.md, tools/beacon-gates/gate/docsgate/docs.go
Зависимости: 
Ссылка: 
Агент: exe124
Время: 2026-11-21T17:59:35Z
Цель: Дать файлам ADR осмысленные имена по заголовку и обновить ссылки.
Контекст: Источник: docs/research/docs-health-audit.md, раздел 3.3 и таблица 7 строка 6. 66 файлов NNNN-untitled.md. 0240-rule-check-without-full-review.md начинается с заголовка про проверку правил; 0090-honest-delivery-unchanged-ladder.md дублирует номер 0090 с 0090-registry-lifecycle.md. Базовый замер и приоритет из отчёта: P1.
Шаги: Выбрать имя по заголовку для каждого файла. Переименовать и обновить все вхождения старого пути. Развести дубль 0090. Добавить rule в docsgate, запрещающую untitled в имени.
Проверка: CI зелёный. Ноль файлов *-untitled.md. Все ссылки на переименованные пути разрешаются.
Приёмка: Поиск по имени файла находит тему ADR. docsgate блокирует повторное появление untitled.
