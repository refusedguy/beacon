# FTE-09. Проверять конфиг OTEL в beacon doctor
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/doctor.go, cmd/beacon/doctor_test.go
Зависимости:
Ссылка: docs/evals/e11-metrics-counters.md:11,20,27, docs/research/research-feature-gaps.md:18,57
Агент: нет
Время: нет
baseline: cmd/beacon/doctor.go:106 выполняет ровно три проверки: checkGo, checkGates и checkStateDir. git grep по doctor.go не находит OTEL или EXPORTER, 0 совпадений. Карточки OBS-07 в docs/tasks/ нет.
Цель: beacon doctor предупреждает о неверном BEACON_OTEL_EXPORTER, как требует eval e11.
Контекст: docs/evals/e11-metrics-counters.md:20 требует, чтобы doctor печатал WARN о неверном BEACON_OTEL_EXPORTER. Строка 27 помечает пропуск этой проверки отдельно от состояния gauges и трассировки. README.md:253 называет опечатку риском: любые значения молча дают local sink. Проверки в doctor пока нет.
Шаги: Добавить проверку, которая читает BEACON_OTEL_EXPORTER и сравнивает значение с допустимым набором. Печатать WARN с именем переменной. Оставить exit code 0 для WARN по правилу RULES-CHECK-CONTRACT.md:19. Добавить табличный тест на пустое значение, otlp и bogus.
Проверка: BEACON_OTEL_EXPORTER=bogus beacon doctor печатает строку с WARN и именем переменной. При пустом значении или otlp предупреждения нет. Код выхода равен нулю в обоих случаях.
Приёмка: eval e11 снимает проверку без пометки пропущено, CI зелёный.
