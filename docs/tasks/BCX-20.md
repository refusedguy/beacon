# BCX-20. Добавить green_blocked в списки вердиктов документации
Статус: done
Фаза: P2
Тип: task
Владелец файлов: docs/GLOSSARY.md, README.md, internal/domain/verdict.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать вердикт green_blocked видимым в контракте для агентов.
Контекст: Источник: docs/research/error-codes-audit.md, P2 «Коды только в тестах» и отдельный случай green_blocked. green_blocked определён в вердикте (verdict.go:883, EvaluateGreenBlocked:910) и используется слотами оркестратора, но отсутствует в обоих списках вердиктов в GLOSSARY.md и README.md. Базовый замер и приоритет из отчёта: P2.
Шаги: Добавить green_blocked в оба списка с описанием: зелёные проверки, но слот оркестратора удержан. Сверить с ADR 0072 и политикой авто-мержа. Добавить тест, что все доменные вердикты перечислены в документации.
Проверка: CI зелёный. Тест сверяет список доменных вердиктов и документацию. green_blocked описан в GLOSSARY.md и README.md.
Приёмка: Агент видит green_blocked в контракте и понимает, что проверки зелёные, а мерж удержан.

Итог: Already landed in PR #1407 (commit 39d04627fffb56b406c498bb2afc46321772a179). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1407 merged 2026-11-21T23:12:11+05:00; commit 39d04627fffb56b406c498bb2afc46321772a179; git merge-base --is-ancestor 39d04627fffb56b406c498bb2afc46321772a179 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1407 39d04627
Время закрытия: 2026-11-22T11:24:28Z
