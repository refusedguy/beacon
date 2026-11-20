# ADR 75: Прямой wake через HTTP API session.prompt с delivery steer

Статус: accepted

## Контекст

Beacon Watch должен будить припаркованную сессию OpenCode при новом факте. beacon_ack лишь записывает намерение и не будит сессию. Живой /openapi.json (info.title "opencode HttpApi", version 0.0.1) определяет POST /api/session/{sessionID}/prompt.

## Решение

Вызывать POST /api/session/{sessionID}/prompt с телом {"text": ..., "delivery": "steer"}. Описание: Durably admit one session input and schedule agent-loop execution unless resume is false. Обязателен только text, additionalProperties false. Ответ 200 с data Session.Inbox.User; data.id совпадает с inboxID.

## Последствия

Плагин не нужен: opencode api обращается к тому же серверу и авторизации, что и TUI. Возможны коды 400/401/404/409. Большинство session-эндпоинтов experimental. Prompt-хук может заменить steer на queue один раз при admission.
