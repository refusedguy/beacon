# ADR 208: Сообщение о merge не равен проверенному merge

Статус: accepted

## Контекст

Тег merged:<PR> в OpenRig — report с recorded_at временем записи строки; истина — integration_ref и ancestry кандидата.

## Решение

Beacon различает claim о слитии и проверенное состояние интеграции и хранит источник этого утверждения.

## Последствия

Squash/rebase не становится предком; report сам по себе не меняет решение о merged.
