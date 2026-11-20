# ADR 177: Snapshot и restore с честными статусами

Статус: accepted

## Контекст

OpenRig различает resumed, rebuilt, fresh-primed, awaiting-decision, attention_required, failed и operator_recovered. Failed resume откатывает пустую сессию.

## Решение

Beacon использует фиксированный набор исходов восстановления и запрещает тихую подмену неудавшегося resume свежим запуском.

## Последствия

Оператор получает явный статус и команду продолжения. Автоматический fresh возможен только по явному флагу.
