# ATX-09. Проверять sender слэш-команд Telegram через ActorResolver
Статус: done
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/up.go, internal/infra/wake/telegram.go, internal/infra/channel/ingress.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не давать неизвестному chat ID менять состояние watch через слэш-команды.
Контекст: Источник: docs/research/security-audit.md, находка 1 и строка таблицы 149. cmd/beacon/up.go:245-256 передаёт сообщения в handleUpCommand, условие в :266 пропускает любое сообщение с /. ParseBotCommand в internal/infra/wake/telegram.go:269-290 не проверяет chat ID. Ветка up.go:284-290 вызывает decisions.Decide для /approve, /unblock и /pause. upDecisionAdapter.Decide в :85-97 не принимает actor. Реестр отправителей живёт в cmd/beacon/server.go:33-37 и применяется в ingress.go:134-137. Severity high.
Шаги: Проверять chat ID в handleUpCommand через тот же ActorResolver, что и channel ingress. Передавать actor в httpapi.ClientDecision. Для команд без actor возвращать отказ и писать его в аудит. Убрать условие !strings.HasPrefix(..., "/") из up.go:266 или дублировать проверку sender в слэш-ветке. Добавить тест на отказ неизвестного chat ID для /approve.
Проверка: CI зелёный. Тест на /approve от неизвестного chat ID ожидаемо отказывает. Слэш-команды без actor не достигают decisions.Decide.
Приёмка: Actor для слэш-команд берётся из реестра отправителей. Журнал аудита содержит отказы.

Итог: Уже влито в PR #1221
Доказательство: 1dd3a2ab в origin/main, merge-base --is-ancestor=0