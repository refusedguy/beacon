# CLEAN-04. Убрать семь устаревших исключений reachability и пересобрать бинарник гейтов
Статус: done
Фаза: P1
Тип: task
Владелец файлов: tools/beacon-gates/gate/config/reachability.json, tools/beacon-gates/beacon-gates
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Удалить семь записей REACH-UNWIRED для символов, которых нет ни в одном Go-файле, и закоммитить пересобранный бинарник гейтов в том же PR.
Контекст: Источник: docs/research/cleanup-plan.md, задача 5. Строки 48-52 ACPWaitService, 58-62 MentionWaitService, 68-72 NewACPWaitService, 98-102 NewMentionWaitService, 128-132 T215Adapter, 134-138 T216Bridge, 140-144 T218Adapter.Конфиг встроен через go:embed в gate/reachability_exceptions.go:13-14. Риск: низкий с нюансом сборки бинарника. Reachability-гейт не входит в required-список ci.yml:190.
Шаги: Проверить отсутствие символов по internal, cmd, tools. Удалить семь блоков из reachability.json. Пересобрать tools/beacon-gates/beacon-gates reproducible флагами из .github/workflows/README.md и закоммитить оба файла вместе.
Проверка: gates-binary в CI сверяет закоммиченный бинарник со сборкой. Локальные go build и гейты не запускать.
Приёмка: Семь записей удалены, бинарник совпадает со сборкой в CI, другие исключения reachability не затронуты.

## Подтверждение
- PR #1161 (https://github.com/refusedguy/beacon/pull/1161): удалены семь записей REACH-UNWIRED для ACPWaitService, MentionWaitService, NewACPWaitService, NewMentionWaitService, T215Adapter, T216Bridge и T218Adapter. Поиск по internal, cmd и tools дал ноль совпадений для каждого символа, поэтому исключения ничего не подавляли. Конфиг остался валидным JSON, число записей permanent упало с 30 до 23, остальные исключения не тронуты.
- Бинарник tools/beacon-gates/beacon-gates пересобран с GOTOOLCHAIN=go1.26.0 флагами из .github/workflows/gates-binary.yml (CGO_ENABLED=0, -trimpath, -buildvcs=false, -ldflags="-buildid= -s -w"). Две независимые сборки байтово совпали, SHA-256 e20054e7e31e1c66d9fd5cde5bf35ce2399b40f2c23f60ad035b541f4f738027. Self-test гейта reachability прошёл с кодом 0.
