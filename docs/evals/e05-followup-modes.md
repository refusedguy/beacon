# E05_followup_modes

## Сценарий

Проверить auto, queue, steer и restart на активном и idle turn.

## Проверки

- auto steers активный
- queue создаёт отдельный заход
- restart останавливает turn

## Негативный контроль

один steer на все случаи теряет сообщение при не-steerable turn
