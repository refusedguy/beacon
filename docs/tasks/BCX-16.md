# BCX-16. Привести документированные вердикты fail и warn к коду
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: docs/runbooks/observability.md, internal/app/orchestrator/orchestrator.go, docs/RULES-CHECK-CONTRACT.md, cmd/beacon/commands.go, cmd/beacon/doctor.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать обещание метрики, значение которой никогда не появляется, и регистровые расхождения отчётов правил.
Контекст: Источник: docs/research/error-codes-audit.md, P1 «fail и warn документированы как вердикты». docs/runbooks/observability.md:60 обещает verdict_published_total{verdict} со значениями ready/retry/fail; в домене fail нет, он есть только как VerdictFail в orchestrator.go:35. docs/RULES-CHECK-CONTRACT.md:42 использует WARN в тексте и warn в JSON, commands.go:61-72 реализует только строчные, doctor.go:176-215 только верхний регистр. Базовый замер и приоритет из отчёта: P1.
Шаги: Либо добавить fail в доменный набор и метрику, либо убрать fail из документации. Выбрать один регистр для отчёта правил и применить его в commands.go и doctor.go. Связать exit code Broken=2 с описанной шкалой.
Проверка: CI зелёный. Документация и код перечисляют одинаковые значения. Тест метрики проверяет реально публикуемые вердикты.
Приёмка: Метрика verdict_published_total не обещает fail, если домен его не пишет. Один регистр для machine и human отчёта.
