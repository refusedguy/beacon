# E03_stale_and_fork

## Сценарий

check_run со старым head_sha, check_run из fork с пустым pull_requests и событие с sender ghost.

## Проверки

- устаревшее событие не меняет вердикт и ревизию
- fork-событие мапится по head_sha, а не по номеру PR
- sender ghost не влияет на маппинг и не открывает доступ
- dispatched refresh для одного Watch схлопывается в один

## Негативный контроль

Маппинг только по PR номеру теряет fork-событие, а доверие sender ghost ломает авторизацию.
