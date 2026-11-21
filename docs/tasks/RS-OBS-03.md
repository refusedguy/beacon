# RS-OBS-03. Метрики цикла и доставки в beacon metrics
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/observe/metrics.go, internal/infra/observe/metrics_test.go
Зависимости: RS-OBS-01
Ссылка: 
Агент: нет
Время: нет
Premise: проверено 2026-11-21 по origin/main (d3b1bfb). Гистограмма длительности цикла уже есть. `HistogramPollCycle = "poll_cycle_duration_seconds"` определена в observe.go:127, замеры идут в observe.go:329 и 336 через PollCycleEnd, рабочий вызов стоит в cibridge/bridge.go:520. Границы зафиксированы в metrics.go:115. Общий код экспорта в JSON и Prometheus в metrics.go:301-347 и 428-481 уже отдаёт эту гистограмму. Счётчики доставки по адаптеру тоже есть: `CounterWakeDelivered` и `CounterWakeErrors` с меткой `adapter` в metrics.go:25-26 и observe.go:98-99, приращения в observe.go:446 и 504. Метки `outcome` в пакете observe нет, счётчика по исходу доставки нет. Третья ступень лестницы блокировок уже считается счётчиками `CounterFixersSpawned` и `CounterBlocksEscalated` в metrics.go:39-43, вызов в orchestrator.go:402. Вторая гистограмма цикла создаст дубль и нарушит запрет на второй экспортер.
baseline: `grep -c 'CounterFixersSpawned' internal/infra/observe/metrics.go` = 3 (строки 39, 43, 89), а не один, как записано в Контексте. `grep -n 'HistogramPollCycle' internal/infra/observe/observe.go internal/infra/observe/metrics.go` = observe.go:127, 329, 336, metrics.go:295. `grep -n 'histogramBounds' internal/infra/observe/metrics.go` = 115, 127, 138, 331, 413, 471. `grep -rn 'outcome' internal/infra/observe --include=*.go` = только комментарии в observe.go:572 и 610. `grep -n 'recordDelivery' internal/infra/cibridge/bridge.go` = 257, 1074, 1081, 1260, 1267, 1564, 1576, 1701, 1705, 1812, 1837. `grep -n 'deliverMaxAttempts' cmd/beacon/deliver.go` = 207, значение 3. `git rev-parse --short origin/main` = d3b1bfb. `gh pr list --repo refusedguy/beacon --state all --search 'RS-OBS-03'` = 0 PR.
Цель: Расширить существующий вывод beacon metrics гистограммой длительности цикла и счётчиками доставок по адаптеру и исходу.
Контекст: Premise: проверено 2026-11-21, метрики уже отдаются в JSON, но длительность цикла и исходы доставки не измеряются. baseline: `grep -n 'CounterFixersSpawned' internal/infra/observe/metrics.go` находит только счётчик FIX. Источник: gen-ai-metrics.md и Hermet idea про context profiling.
Шаги: Добавить гистограмму с фиксированными границами и счётчики по адаптеру и исходу. Не создавать второй экспортер. Сохранить формат JSON совместимым. Тест на нулевые и ненулевые значения.
Проверка: gofmt чистый. Тесты в CI. Старые потребители JSON не ломаются.
Приёмка: Метрика отвечает на вопрос, какой адаптер чаще даёт transport-failed и сколько раз лестница доходит до третьей ступени.

## Блокер

- 2026-11-21. Две части задачи уже сделаны в main, третья требует чужих OwnerFiles.

  Гистограмма длительности цикла с фиксированными границами работает и экспортируется в JSON и Prometheus. Добавить её ещё раз в metrics.go значит создать дубль `poll_cycle_duration_seconds`. Счётчики доставки по адаптеру `wake_delivered_total` и `wake_errors_total` уже считают поставленные и неудачные wake с меткой `adapter`. Третья ступень лестницы блокировок уже видна через `fixers_spawned_total` и `blocks_escalated_total`.

  Исход доставки в метриках отсутствует. Константы счётчиков и ключи меток лежат в internal/infra/observe/observe.go:88-140. Хелперы `WakeDeliveredSource` и `WakeFailed` в observe.go:444-451 и 502-515 принимают адаптер и ошибку, но не исход. Значение исхода вычисляется в internal/infra/cibridge/bridge.go:1070-1077 и 1560-1576 и в cmd/beacon/deliver.go:150-203 через classifyPrompt. Чтобы счётчик по адаптеру и исходу работал, нужно изменить сигнатуры хелперов в observe.go и их вызовы в cibridge/bridge.go и cmd/beacon/deliver.go. Все три файла вне OwnerFiles карточки. Счётчик, объявленный только в metrics.go, не будет увеличиваться и не ответит на вопрос приёмки про transport-failed и третью ступень.

  Статус todo сохранён. Код не писал, gofmt не применял, OwnerFiles не менял, дубль не создал, wiring и .beacon не трогал. Блокер возвращён оркестратору: для карточки нужны OwnerFiles с internal/infra/observe/observe.go, internal/infra/cibridge/bridge.go и cmd/beacon/deliver.go либо переформулировка шагов под уже существующие метрики.

## Подтверждение
- PR #876 MERGED: 2026-11-21 (squash).
