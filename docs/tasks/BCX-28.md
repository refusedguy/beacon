# BCX-28. Свести три описания --interval и исправить daemon --once
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/daemon.go, cmd/beacon/up.go, cmd/beacon/main.go, README.md
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Один источник значения интервала и честный код выхода для daemon --once.
Контекст: Источник: docs/research/cli-ux-audit.md, таблица 5 строка 8. README.md:202 обещает три минуты, usageText:42 пишет 10s|30s, комментарий daemon.go:172 пишет 3m; код daemon.go:177 использует 0 как адаптивные 10s/30s. daemon --once всегда возвращает 0 (daemon.go:238-244). up.go:143 имеет --once, но up не подключён к dispatch. Базовый замер и приоритет из отчёта: P2.
Шаги: Вынести default в одну константу и использовать её в коде, usage и README. Передавать результат ReconcilePass в код выхода. После BCX-23 проверить up --once.
Проверка: CI зелёный. Тест daemon --once даёт ненулевой код при неудачном проходе. Три источника описания совпадают.
Приёмка: Интервал имеет одно описание. Неудачный проход меняет код выхода.

Итог: Already landed in PR #1301 (commit e437af7bc9d386ac34e9d8f90438e88ae4be2729). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1301 merged 2026-11-22T08:56:04+05:00; commit e437af7bc9d386ac34e9d8f90438e88ae4be2729; git merge-base --is-ancestor e437af7bc9d386ac34e9d8f90438e88ae4be2729 origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1301 e437af7b
Время закрытия: 2026-11-22T11:24:28Z
