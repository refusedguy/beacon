---
name: red-verdict-triage
description: Classify a red or retry Beacon verdict against your own diff and wake the orchestrator. Use when a subagent wakes on verdict retry or red.
---

# Red verdict triage

Исполнитель просыпается на вердикте `retry`, когда CI красный. Сначала он определяет причину, потом шлёт wake оркестратору. До классификации он не меняет код и не трогает `main`.

## Процедура

1. Прочитать снимок вызовом `beacon_get_result`. В ответе есть `failedChecks`. Каждая запись несёт `name`, `conclusion`, `detailsUrl`, `required` и `flakyMatched`. Полные логи не нужны.
2. Сверить `failedChecks` со своим diff. Посмотреть `git status --short` и `git diff <baseRef>..HEAD` в своём worktree. Проверка считается твоей, когда её причина лежит в файлах, которые ты менял в этой ветке.
3. Выбрать одну из трёх причин и отослать wake подписчику `role=orchestrator` на том же Watch. Причина уходит в текст wake, оркестратор видит её под полем `verdict`.
   - Записи `failedChecks` лежат вне твоего diff, либо `main` красный на последнем коммите. Это флейк или сломанный `main`. Причина: `братан иди чини main`.
   - Причина падения лежит в твоём diff. Это твоя ошибка. Причина: `моя вина, чиню`. После wake исправить свою ветку и запушить новый headSHA.
   - Причину нельзя уверенно отнести ни к тому, ни к другому. Причина: `проверил, изменений моих нет`.
4. Завершить ход. Wake оркестратору не означает готовность PR. Мерж делает только оркестратор после своего `beacon_get_result` с `verdict=ready`.

Для отправки использовать `wake.NotifyOrchestrator(ctx, reason, p)`. Функция сама находит сессию оркестратора по подписчику `role=orchestrator` на Watch из `p.WatchID` и обрезает пробелы вокруг причины.

## Запрет

Не чинить `main` самому. Не пушить в `main`, не делать force-push в общие ветки и не править файлы вне своего worktree. Если `main` красный, оркестратор получает wake и сам решает, что делать.
