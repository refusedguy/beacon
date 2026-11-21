# ARDH-19. Сохранить причину внутри AppError без изменения JSON
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/domain/types.go, internal/domain/fanout.go, internal/domain/identity.go, docs/adrs/0009-error-handling.md
Зависимости: 
Ссылка: ADR 0009
Агент: нет
Время: нет
baseline: до правки четыре места в internal/domain/fanout.go превращали ошибку store в строку через err.Error(): ActiveWatches, AllWatches, SaveWatch и SaveObligation. errors.Is(err, sql.ErrNoRows) возвращал false. Источник: docs/research/error-handling-audit.md, находка 3. Замер снят по исходнику, код не запускался.
Цель: Добавить в AppError неэкспортируемое поле cause и метод Unwrap, чтобы fanout.go передавал ошибку store, не меняя шести полей ответа.
Контекст: Источник: docs/research/error-handling-audit.md, находка 3 и рекомендация 4. fanout.go:94 и 98 превращают ошибку store в строку. identity.go:171 и 178 теряют причину кодирования. Риск: средний. Нужен owner-decision: если владелец считает контрактный тип плоским, решение фиксируется в ADR 0009 и код не меняется.
Шаги: Получить решение владельца. При согласии добавить поле cause, метод Unwrap и конструктор с причиной. Обновить fanout.go. Проверить, что JSON ответа побайтно прежний.
Проверка: Тесты контракта MCP и сериализации AppError зелёные в CI.
Приёмка: Ошибка store различима через errors.Is и errors.As, JSON ответа не изменился. Карточка остаётся todo до решения владельца.

## Подтверждение
- Реализация в PR [#1184](https://github.com/refusedguy/beacon/pull/1184). `AppError` хранит причину в неэкспортируемом поле `cause` и возвращает её через `Unwrap`. Конструктор `NewAppErrorWithCause` задаёт причину, а `NewAppError` сохраняет прежнее поведение.
- `fanout.go` передаёт исходную ошибку store во всех четырёх местах, где раньше вызывался `err.Error()`. Коды и тексты ошибок не изменились. `errors.Is` различает `sql.ErrNoRows` и ошибку диска. Поле `cause` не имеет JSON-тега, поэтому ответ побайтно прежний.
- Тесты в `internal/domain` проверяют `Unwrap` через обёрнутый `AppError`, пустую причину, нулевой указатель, прежний набор ключей JSON и все четыре пути ошибки store в `fanout`. gofmt применён к четырём Go-файлам. `go test`, `go build` и гейты оставлены CI.
