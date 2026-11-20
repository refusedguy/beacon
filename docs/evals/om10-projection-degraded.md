# OM10_projection_degraded

## Сценарий

В проекции my-queue два зарегистрированных человека и нет operatorSession.

## Проверки

- View пуст
- degradedFields объясняет причину
- Нет wildcard fallback

## Негативный контроль

Система показывает чужие строки как «мои».
