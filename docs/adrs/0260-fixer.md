# ADR 260: Fixer как отдельная роль подписчика

Статус: accepted

## Контекст

ADR 0155 вводит blocked Watch и wake с текстом spawn FIX-agent. Оркестратор спавнит FIX-агента, но агент регистрируется как executor: в домене и в WatchPRInput есть только роли executor и orchestrator. Из-за этого не видно, какую заблокированную Watch чинит агент, и метрики не отличают фикс от обычного retry.

## Решение

Ввести третью роль подписчика fixer. Она ведёт себя как executor: получает wake на retry и rebase, адресуется по sessionID и держит обязательство на своей Watch. Дополнительно fixer хранит fixFor с ID заблокированной Watch, которую чинит. Поле лежит в domain.Subscriber, domain.Watch и в опциональном WatchPRInput.fixFor. Register принимает role=fixer только с непустым fixFor. Значение должно называть существующую Watch в статусе blocked. Несуществующая Watch даёт WATCH_NOT_FOUND, Watch вне blocked даёт INVALID_INPUT. Оба случая это ошибки, а не предупреждения: счётчик фиксов должен указывать на реальную блокировку. fixFor на роли executor или orchestrator тоже отклоняется. Оркестратор читает вердикт и revision перед спавном, поэтому гонка со снятием блокировки приводит к отказу регистрации и повторному спавну. DaemonWakeSubscriber выбирает executor и fixer. Orchestrator по-прежнему идёт отдельным маршрутом blocked wake. Другие проверки роли в wake/deliver.go и cibridge/bridge.go пока перечисляют только executor и orchestrator, их расширение вынесено в отдельную карточку.

## Последствия

Фикс перестал быть безымянным: Watch и подписчик хранят fixFor, а роль fixer отделяет эту работу от обычного retry. Метрики считают фиксы по роли fixer; отдельный счётчик вынесен в карточку. Вызовы beacon_watch_pr с role executor или orchestrator не меняются, у них fixFor пуст. Схема входа остаётся совместимой: fixFor в схеме опционален, а требование к нему проверяет граница инструмента до любой записи в store.
