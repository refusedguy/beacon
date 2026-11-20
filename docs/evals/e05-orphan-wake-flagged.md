# E05_orphan_wake_flagged

## Сценарий

Watch подписан на sessionID, которого нет в GET /api/session и GET /api/session/active.

## Проверки

- wake_delivered содержит orphan=true
- orphan_wake_total увеличен
- beacon status показывает запись в orphanWakes

## Негативный контроль

Живая сессия в active не помечается orphan.
