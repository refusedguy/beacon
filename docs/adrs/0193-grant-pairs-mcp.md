# ADR 193: Классы доступа и grant pairs для инструментов MCP

Статус: accepted

## Контекст

MCP сервер OpenRig регистрирует 39 инструментов условием offered(tool). В MCP_OPERATIONS у каждого инструмента effect и access class owner/work/workspace/person.

## Решение

Beacon задаёт access class и effect для каждого инструмента, а внешнему grant перечисляет пары объект/действие, а не один общий bearer.

## Последствия

Инструмент вне grant не регистрируется. Listed tool не равняется разрешению, это прямо сказано в member workflow.
