---
name: night-shift
description: Triage and clear GitHub issues in the night shift. Use when working the night-shift backlog.
---

# Night shift

## Процедура разбора issue

1. Прочитать issue и все существующие комментарии: `gh issue view N --comments`. Не дублировать уже сказанное.
2. Определить карточку, зависимости и владельцев файлов. Работать в своей ветке и worktree, не трогать файлы вне карточки.
3. Сделать фичу, открыть PR в `main` и дождаться CI. Перед мержем прочитать `references/pr-readiness.md`.
4. Комментировать только при новом факте. Перед комментарием прочитать `references/comment-rules.md`.
5. Перед закрытием issue сверить комментарии и статус PR, устранить противоречия.
