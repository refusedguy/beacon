# Beacon handoff

Словарь домена цикла Beacon: кто работает над карточкой, кто наблюдает за PR и как executor выходит из работы в ожидание и завершение.

## Language

### Роли

**Executor**:
Агент, которому принадлежит карточка и PR и который отвечает за результат своей работы.
_Avoid_: worker, coder, subagent

**Orchestrator**:
Тонкая сессия, которая читает вердикты Watch и запускает FIX-агента, когда падение проверок не относится к executor. Оркестратор не чинит main сам.
_Avoid_: coordinator, менеджер задач, main

**Beacon**:
Инструмент наблюдения и доставки указателей просыпания. Beacon не чинит main и не подменяет агента.
_Avoid_: daemon as actor, controller, automated fixer

### Состояния executor

**В работе (Working)**:
Executor активно ведёт карточку: меняет код, пушит ветку, открывает или обновляет PR.
_Avoid_: active, running, in flight

**Жду (Waiting)**:
PR сдан, мониторинг активен, executor спит до вердикта, который разрешает пробуждение.
_Avoid_: idle, parked, watching

**Закончил (Completed, Detached)**:
PR залит, executor отвязан от мониторинга и больше ничего не требуется. Состояние остаётся видимым: переход навсегда повышает observability.
_Avoid_: done, finished, closed

### Наблюдение и сигналы

**Watch**:
Единица наблюдения за PR с версией и вердиктом; executor и orchestrator считывают её состояние.
_Avoid_: монитор, отслеживание, PR check

**Wake**:
Указатель просыпания, который доставляется подписчику при изменении состояния Watch.
_Avoid_: notification, уведомление, prompt

**blocked:not_mine**:
Сигнал executor о том, что упавшая проверка не относится к его PR. Orchestrator просыпается по этому сигналу и спавнит FIX-агента.
_Avoid_: чужое падение, external failure, retry

### Связи и ёмкость

**orchestrator id**:
Идентификатор родительской сессии, закреплённый за executor. Пока хранится как связь и не участвует в маршрутизации wake.
_Avoid_: parentID, parent session, owner

**rebind**:
Смена orchestrator id у executor при смене родительской сессии. Пока не маршрутизирует wake.
_Avoid_: перепривязка, reassignment, reparenting

**slot**:
Ёмкость orchestrator по числу executor, которых он может вести одновременно.
_Avoid_: concurrency limit, лимит параллельности, worker pool
