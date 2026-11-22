# PSKX-05. Реестр формальных проверок с записью покрытия
Статус: done
Фаза: P8
Тип: doc
Владелец файлов: docs/runbooks/verification.md
Зависимости: 
Ссылка: https://raw.githubusercontent.com/cursor/plugins/main/pstack/skills/create-formal-verification-skill/SKILL.md
Агент: ses_pskx05_executor
Время: 2026-11-22T17:19:13Z
Цель: Хранить вместе с проверкой запись покрываемых файлов и функций, команду запуска и commit, на котором проверка последний раз сверена с кодом; двигать commit только после сверки.
Контекст: Источник: `pstack/skills/create-formal-verification-skill/SKILL.md:29` (Record which files and functions). `docs/runbooks/verification.md:50-62` перечисляет проверки скриптов, но не хранит для них покрываемые файлы и commit синхронизации. PSK-03 создаёт TLA-модель Watch, но не реестр проверок и не поле sync commit. baseline: `git grep -i -E 'запись покрытия|commit синхронизации' -- docs/runbooks/verification.md | wc -l` = 0 на `origin/main`. Карточка фиксирует приём в дереве beacon, не создаёт новый глобальный скилл и не меняет протокол Watch. Не дублирует PSK-01 (кандидаты), PSK-02 (адверсарии), PSK-03 (TLA Watch), CP-01..05, CM-01..05 или закрытые дубли HD-099/HD-105/HD-109. Риск: низкий.
Шаги: Добавить в runbook раздел «Реестр проверок» с колонками: свойство, файлы и функции, границы, команда, commit синхронизации. Записать туда существующие проверки `verify-beacon.sh` и `verify-daemon.sh`. Запретить считать проверку актуальной, если покрываемый файл изменился после commit синхронизации. Требовать негативный контроль наконтрольной правке перед jede s judgment.
Проверка: `git grep -n -E 'запись покрытия|commit синхронизации' docs/runbooks/verification.md` находит раздел и колонки таблицы.
Приёмка: Для каждой проверки указаны файлы, команда и commit. Проверка без негативного контроля не считается доставленной.

Причина: Уже влито в origin/main: PR #1740, squash-коммит da1d710c54302a8e20e6a012b20d06628f1265f6; git merge-base --is-ancestor da1d710c54302a8e20e6a012b20d06628f1265f6 origin/main -> exit 0. docs/runbooks/verification.md уже содержит раздел «Реестр проверок» с колонкой «Commit синхронизации». Дубль кода не создаётся.
Закрыл: ses_pskx05_executor
Время закрытия: 2026-11-22T17:19:54Z
