---
name: night-shift-manual
description: Manually invoke night-shift operations that need a human, such as posting an issue comment, closing an issue, or merging a PR. The model must not call this skill on its own.
disable-model-invocation: true
---

# Ручные операции ночной смены

Скилл вызывается только человеком. Поле `disable-model-invocation: true` в frontmatter запрещает модели запускать его по случайной фразе в чате.

Фоновый разбор issue живёт в отдельном скилле `../SKILL.md` без этого поля. Если нужно разобрать issue без действия, используйте его.

Порядок ручной операции:

1. Прочитать issue и связанный PR.
2. Сформулировать новый факт: хеш влитого PR, измерение, решение владельца или блокер.
3. Написать комментарий только при новом факте.
4. Закрывать issue или мержить PR только после явного подтверждения человека.
5. Сообщить результат в чат фактами: номер, действие, доказательство.
