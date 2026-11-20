# W09_honest_delivery

## Сценарий

Честная доставка. В store четыре разных исхода: steer, queue, unsupported, transport-failed.

## Проверки

- В store есть steer, queue, unsupported и transport-failed
- Лестница не ретраит posted и indeterminate
- transport-failed ретраится тем же указателем

## Негативный контроль

Подменить queue на posted.posted не ретраится, кап три попытки через 5 и 10 минут.
