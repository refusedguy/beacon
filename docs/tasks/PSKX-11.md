# PSKX-11. Разделение дрейфа проверки и бага в коде
Статус: done
Фаза: P8
Тип: doc
Владелец файлов: docs/runbooks/verification.md
Зависимости: 
Ссылка: https://raw.githubusercontent.com/cursor/plugins/main/pstack/skills/maintain-formal-verification-skill/SKILL.md
Агент: ses_pskx11b_executor
Время: 2026-11-22T18:08:02Z
Цель: Для каждой проверки различать четыре исхода: в синхроне, обновлена, баг в коде, заблокирована; обновлять только проверку и never ослаблять свойство ради зелёного прогона.
Контекст: Источник: `pstack/skills/maintain-formal-verification-skill/SKILL.md:13-20` (in sync). `docs/runbooks/verification.md:28-32` описывает перезапускаемость скриптов и коды выхода, но не различает провал из-за устаревшего описания и провал из-за реального бага. PSK-03 создаёт модель, но не процедуру её обновления после изменений кода. baseline: `git grep -i -E 'дрейф проверки|баг в коде' -- docs/runbooks/verification.md | wc -l` = 0 на `origin/main`. Карточка фиксирует приём в дереве beacon, не создаёт новый глобальный скилл и не меняет протокол Watch. Не дублирует PSK-01 (кандидаты), PSK-02 (адверсарии), PSK-03 (TLA Watch), CP-01..05, CM-01..05 или закрытые дубли HD-099/HD-105/HD-109. Риск: низкий.
Шаги: Добавить в runbook таблицу исходов проверки: в синхроне, обновлена, баг, заблокирована. Запретить правку свойства или границ ради прохождения. Требовать обновить модель из кода до повторного прогона и показать, что обновлённая проверка всё ещё умеет падать на controlной правке.
Проверка: `git grep -n -E 'дрейф проверки|баг в коде' docs/runbooks/verification.md` находит таблицу исходов и запрет на ослабление.
Приёмка: Провал проверки gets один из четырёх исходов с указанием commit. Ослабление свойства без доказательства изменения обещания проваливает карточку.

Причина: Fix already landed in origin/main through PR #1809, commit 35545a0afe33f6d875be6a3338964491274a4b8e, docs(verification): четыре исхода провала проверки (PSKX-11). git merge-base --is-ancestor 35545a0a origin/main exits 0. docs/runbooks/verification.md carries the four-outcome table. No duplicate code PR; this close records that the work shipped.
Закрыл: ses_pskx11b_executor
Время закрытия: 2026-11-22T18:08:33Z
## Подтверждение
- PR #2077 MERGED: 2026-11-22 (squash).

