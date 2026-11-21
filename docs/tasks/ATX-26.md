# ATX-26. Объединить workflow по подготовке раннера
Статус: done
Фаза: P1
Тип: task
Владелец файлов: .github/workflows/ci.yml, .github/workflows/docs.yml, .github/workflows/gates-binary.yml, .github/workflows/tests.yml
Зависимости: 
Ссылка: 
Агент: exe101
Время: 2026-11-21T17:54:38Z
Причина: Уже влито в origin/main через PR #1237, коммит f33d0751769f22632ff6f5eca47dbc0e855772fc ("ci: share Go setup across workflows"); git merge-base --is-ancestor f33d0751769f22632ff6f5eca47dbc0e855772fc origin/main дал код выхода 0. Общий composite action .github/actions/go-runner-setup/action.yml убирает повтор setup-go в ci.yml, docs.yml, gates-binary.yml и tests.yml. Дубль кода не нужен.
Закрыл: exe101
Время закрытия: 2026-11-21T17:56:22Z
Цель: Убрать повтор checkout и setup-go в четырёх близких workflow.
Контекст: Источник: docs/research/ci-speed-audit.md, строка таблицы 92 и раздел Текущая раскладка. CI, Registry check, Docs и Gates binary поднимают отдельные раннеры. Выигрыш оценён в 6-10 с на setup и один раннер на PR, меняющий гейты или документацию. В отчёте нет метки P0/P1, приоритет P1 взят из оценки риска Средний.
Шаги: Объединить gates-binary и docs в ci.yml как джобы с условием по путям. Сохранить фильтры и коды выхода. Сохранить информационный статус docs и набор обязательных проверок batchapply.
Проверка: После мержа проверить, что PR с правкой гейтов и PR с правкой docs запускают нужные джобы. Сверить медиану времени по 10 прогонам.
Приёмка: Один раннер вместо двух на PR с гейтами или docs. Фильтры путей и статусы проверок не потеряны.
