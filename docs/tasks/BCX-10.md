# BCX-10. Определить статус research-отчётов в git
Статус: done
Фаза: P3
Тип: task
Владелец файлов: .gitignore, docs/research/
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Явно решить, хранятся ли research-отчёты в репозитории или остаются временными.
Контекст: Источник: docs/research/docs-health-audit.md, таблица 7 строка 10. git ls-files docs/research возвращает 0. Каталог не игнорируется, но отчёты не добавлены. В снимке отчёта семь файлов. Базовый замер и приоритет из отчёта: P3.
Шаги: Выбрать политику с владельцем. Если отчёты нужны, закоммитить их отдельным коммитом. Если временные, добавить docs/research/ в .gitignore и сделать правило явным.
Проверка: CI зелёный. git status в чистом дереве не показывает docs/research как неотслеживаемые, если выбрана политика ignore.
Приёмка: Политика зафиксирована в .gitignore или в коммите. Новый research-отчёт не создаёт шум в git status.

Итог: Already landed in PR #1268 (commit b262ba39686e49bd86097a0ed560df17afceacf9). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1268 merged 2026-11-21T21:54:37+05:00; commit b262ba39686e49bd86097a0ed560df17afceacf9; git merge-base --is-ancestor b262ba39686e49bd86097a0ed560df17afceacf9 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1268 b262ba39
Время закрытия: 2026-11-22T11:24:28Z
