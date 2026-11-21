# ATX-19. Добавить single-flight в cibridge.Refresh
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/cibridge/bridge.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не строить два решения над одним снимком Watch.
Контекст: Источник: docs/research/concurrency-audit.md, раздел 1 и строка таблицы 138. internal/infra/cibridge/bridge.go:405 зовёт Refresh сразу и дальше по тикеру. Refresh в :508 экспортирован. rebased и rebasedMu в :166-167, breaker и breakerMu в :172-173 защищают карты, но единой блокировки на вызов нет. Две параллельные копии перемешивают счётчики PollCycleStart и PollCycleEnd в :520 и :528. Приоритет средний в отчёте.
Шаги: Поставить refreshMu sync.Mutex в начало Refresh и держать до конца цикла. Либо сделать atomic CAS флаг single-flight с возвратом 0 для второго вызова.
Проверка: CI зелёный. Тест с двумя горутинами, зовущими Refresh на одном Bridge и store, даёт ровно одно wake на (watchID, revision). Прогон -race -count=50 зелёный.
Приёмка: Одновременно исполняется только один Refresh. Счётчики цикла не перемешиваются.

Итог: Уже влито в PR #1227
Доказательство: 275ef340 в origin/main, merge-base --is-ancestor=0