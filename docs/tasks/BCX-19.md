# BCX-19. Привести написание machine-кодов в тестах и документации к продовым
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/wake/notify_test.go, cmd/beacon/up_test.go, docs/gates/G2-canary.md, internal/domain/verdict_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить «not mine», «head changed» и «green-blocked» на коды из кода.
Контекст: Источник: docs/research/error-codes-audit.md, P2 «Опечатки и варианты написания». not mine в notify_test.go:364 и up_test.go:115,136,145 вместо not_mine; head changed в docs/gates/G2-canary.md:73 вместо head_changed; green-blocked в verdict_test.go:1497,1555 против green_blocked в коде. Базовый замер и приоритет из отчёта: P2.
Шаги: Заменить варианты написания в тестах и документации. Добавить линтер или тест, который находит старые формы в docs и *_test.go. Сохранить WARNING только там, где это префикс более длинной строки.
Проверка: CI зелёный. Поиск старых форм не даёт совпадений вне разрешённых исключений. Тесты.use machine-коды.
Приёмка: Один код имеет одно написание в коде, тестах и документации.

Итог: Already landed in PR #1397 (commit 145c5bd7af3063cd5498a14177b73ae920781b50). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1397 merged 2026-11-22T09:41:31+05:00; commit 145c5bd7af3063cd5498a14177b73ae920781b50; git merge-base --is-ancestor 145c5bd7af3063cd5498a14177b73ae920781b50 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1397 145c5bd7
Время закрытия: 2026-11-22T11:24:28Z
