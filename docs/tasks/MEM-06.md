# MEM-06. Добавить TTL-reaper для карточек in_progress без живого Watch
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/taskcli/service.go, internal/app/taskcli/service_test.go
Зависимости: AUTOSP-06
Ссылка: 
Агент: нет
Время: нет
Цель: Освобождать слот, когда процесс воркера мёртв.
Контекст: Источник: docs/research/audit-02-memory.md, строка таблицы про TTL-reaper. internal/app/taskcli/service.go:483 описывает Take, который ставит статус in_progress. Константа LeaseTTL в :1904 равна тридцати минутам и относится только к glob-арендам, а не к карточкам. Поиск реaper по in_progress в taskcli и taskstore не дал продакшен-вызовов. Вызов из цикла добавляет MEM-06 в mcp.go через адаптер в MEM-04.
Шаги: Добавить метод ReapStaleCards с внедряемыми часами, TTL и портом проверки живого Watch по card_links. Читать карточки in_progress, брать время из поля Время, пропускать карточки с живым Watch или живым процессом, остальные переводить в todo и писать запись в журнал. Сделать порт проверки функции-аргументом, чтобы сервис не зависел от конкретного store. Покрыть service_test.go тестами на живую карточку, мёртвую и на идемпотентность повторного прохода.
Проверка: CI зелёный. Тест с живым Watch оставляет карточку in_progress. Тест с мёртвым воркером переводит её в todo один раз.
Приёмка: Слот освобождается по TTL. Card_links и живые Watch защищают активную работу.

Итог: Already landed in PR #1465 (commit 5597372fac5c26150888eb4f627a93dfd19e16ac). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1465 merged 2026-11-22T10:04:03+05:00; commit 5597372fac5c26150888eb4f627a93dfd19e16ac; git merge-base --is-ancestor 5597372fac5c26150888eb4f627a93dfd19e16ac origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1465 5597372f
Время закрытия: 2026-11-22T11:25:31Z
