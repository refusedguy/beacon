# ADR 189: Capability discovery, privilege ceiling и typed failures

Статус: accepted

## Контекст

T3 orchestrator_capabilities возвращает провайдеров, модели, режимы и feature flags, а недоступный провайдер объясняет причину; определены 14 typed failures. Дочерний runtime mode только ужесточается, interaction mode только default к plan. Crush даёт сабенту read или write без вложенных сабентов. OMA egress policy позволяет детям только ужесточать и fails closed.

## Решение

Добавить в Beacon явное перечисление возможностей адаптеров и причин недоступности, ограничить права дочерней работы относительно родителя и заменить текстовые ошибки типизированными результатами.

## Последствия

Модель видит, почему цель недоступна, до вызова. Нельзя поднять режим ребёнка выше родителя. Неприменимый transport fails closed. Проверка capability остаётся в handler, а не только на уровне socket.
