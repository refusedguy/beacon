# W20_mcp_access_class

## Сценарий

Внешний grant с парами queue/create и queue/list пытается вызвать instrument person class и спросить чужие rig.

## Проверки

- Person tool не зарегистрирован
- Чужой rig вне scope
- listed tool не трактуется как access

## Негативный контроль

Дать доступ по факту присутствия имени инструмента в списке.
