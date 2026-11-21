# CLEAN-09. Закрыть RS-OBS-02 и удалить локальный OTEL sink
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/otel/, docs/tasks/RS-OBS-02.md
Зависимости: RS-OBS-02
Ссылка: 
Агент: нет
Время: нет
Цель: Подтвердить, что observability переехала в internal/infra/observe, закрыть RS-OBS-02 и удалить дублирующий локальный sink.
Контекст: Источник: docs/research/deadcode-audit.md, строка про internal/infra/otel. 410 не-тестовых строк, 0 импортёров. Реализация в internal/infra/observe/otel.go имеет 31 импортёра. Риск: средний. Нужен owner-decision по судьбе карточки RS-OBS-02, которая ещё todo.
Шаги: Сверить функции старого и нового sink. Закрыть RS-OBS-02 через batch-apply с указанием PR. Удалить каталог internal/infra/otel после закрытия карточки.
Проверка: CI зелёный, RS-OBS-02 закрыта, импортов internal/infra/otel нет.
Приёмка: Старый sink удалён только после закрытия RS-OBS-02. До решения владельца карточка остаётся todo.
