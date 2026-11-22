# FTE-09. Проверять конфиг OTEL в beacon doctor
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/doctor.go, cmd/beacon/doctor_test.go
Зависимости:
Ссылка: docs/evals/e11-metrics-counters.md:11,20,27, docs/research/research-feature-gaps.md:18,57
Агент: fte09-executor
Время: 2026-11-22T16:36:23Z
baseline: cmd/beacon/doctor.go:106 выполняет ровно три проверки: checkGo, checkGates и checkStateDir. git grep по doctor.go не находит OTEL или EXPORTER, 0 совпадений. Карточки OBS-07 в docs/tasks/ нет.
Цель: beacon doctor предупреждает о неверном BEACON_OTEL_EXPORTER, как требует eval e11.
Контекст: docs/evals/e11-metrics-counters.md:20 требует, чтобы doctor печатал WARN о неверном BEACON_OTEL_EXPORTER. Строка 27 помечает пропуск этой проверки отдельно от состояния gauges и трассировки. README.md:253 называет опечатку риском: любые значения молча дают local sink. Проверки в doctor пока нет.
Шаги: Добавить проверку, которая читает BEACON_OTEL_EXPORTER и сравнивает значение с допустимым набором. Печатать WARN с именем переменной. Оставить exit code 0 для WARN по правилу RULES-CHECK-CONTRACT.md:19. Добавить табличный тест на пустое значение, otlp и bogus.
Проверка: BEACON_OTEL_EXPORTER=bogus beacon doctor печатает строку с WARN и именем переменной. При пустом значении или otlp предупреждения нет. Код выхода равен нулю в обоих случаях.
Приёмка: eval e11 снимает проверку без пометки пропущено, CI зелёный.

Причина: Code already in origin/main: commit 0a04c1ace5a5ce52b1c33cb7edcb6dc068ece9ff, PR #1767 (MERGED 2026-11-22T13:45:05Z), touching cmd/beacon/doctor.go and cmd/beacon/doctor_test.go. git merge-base --is-ancestor 0a04c1ac origin/main exit 0. No duplicate code PR; this PR carries only the card close.
Закрыл: fte09-executor
Время закрытия: 2026-11-22T16:37:05Z