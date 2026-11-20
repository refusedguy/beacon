# fixer role

## Сценарий

Заблокированная Watch W1 ждёт чужую проверку. Оркестратор спавнит агента, который регистрируется с role=fixer и fixFor=W1 на своей Watch W2. После регистрации демон шлёт ему retry.

## Проверки

- Register с role=fixer и fixFor=W1 сохраняет fixFor на W2 и на подписчике.
- Register с role=fixer без fixFor возвращает INVALID_INPUT и не создаёт Watch.
- Register с fixFor на несуществующую Watch возвращает WATCH_NOT_FOUND.
- Register с fixFor на Watch в статусе watching возвращает INVALID_INPUT.
- DaemonWakeSubscriber выбирает fixer с валидным sessionID и доставляет wake на его сессию.

## Негативный контроль

Убрать проверку fixFor из Register: роль fixer сохранится с пустым адресом, и метрика фиксов станет считать пустые ссылки.
