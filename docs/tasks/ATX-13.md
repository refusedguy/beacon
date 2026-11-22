# ATX-13. Требовать подтверждения владельца для merge_decisions
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/batchapply/apply.go, cmd/batch-apply/main.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Не мержить PR по данным исследовательского документа без явного подтверждения и полного набора проверок.
Контекст: Источник: docs/research/security-audit.md, находка 5 и строка таблицы 153. internal/infra/batchapply/apply.go:477-529 выполняет gh pr merge из merge_decisions. Условие в :514 разрешает merge при пустом списке проверок: len(checks) > 0 && green < len(required). Ошибка no checks reported превращается в пустой список в :487-489. AGENTS.md запрещает batch-apply без команды владельца, код запрет не проверяет. Severity medium.
Шаги: Требовать непустой список проверок для любого PR с изменением кода. Подтверждать doc-only PR по changed files, а не по тексту ошибки gh. Добавить флаг --confirm-merge и проверять его в Run до вызова applyMergePlan. Логировать каждое решение с хешем исходного JSON.
Проверка: CI зелёный. Запуск --merge-plan без --confirm-merge отказывается до вызова gh. Тест с пустым списком проверок и кодом PR удерживает merge.
Приёмка: Merge невозможен без флага владельца. Doc-only STATUS подтверждается по файлам.

Итог: Already landed in PR #1369 (commit c71ca2cc9d67cb07491bf4a4658b7261ad6cc8f1): fix(batchapply): require owner confirmation before merge (ATX-13) (#1369)
Доказательство: PR #1369; commit c71ca2cc9d67cb07491bf4a4658b7261ad6cc8f1; git merge-base --is-ancestor c71ca2cc9d67cb07491bf4a4658b7261ad6cc8f1 origin/main exit 0; no duplicate code PR needed.
Причина: already landed in PR #1369 c71ca2cc9d67
Время закрытия: 2026-11-22T11:27:01Z
