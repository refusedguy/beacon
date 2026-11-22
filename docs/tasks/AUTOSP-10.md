# AUTOSP-10. Eval автоспавна без ручного пинка
Статус: done
Фаза: P2
Тип: eval
Владелец файлов: evals/main.go, evals/README.md
Зависимости: AUTOSP-04
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Измерить число ручных действий на волну и зафиксировать регресс автоспавна.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 10. eval-реестр живёт в evals/main.go:45 и :73. План проверки wake описан в docs/research/wake-via-prompt-api.md:106-134. baseline: eval на терминальный вердикт и последующий spawn отсутствует, число ходов оркестратора не измерялось.
Шаги: Добавить сценарий с Watch в терминальном вердикте, свободным слотом и доступной todo-карточкой. Assertions проверяют один spawn, одну связь cardID и watchID, отсутствие сообщения владельца. Negative control: занятый слот не порождает spawn.
Проверка: CI зелёный. Eval проходит на зафиксированном снимке состояния и не требует живой сети.
Приёмка: Отчёт eval содержит число spawn на один терминальный вердикт.

Итог: Already landed in #1478 (0ef4bf205960391caa95d6f75a540280abc2eb31). No duplicate code needed.
Доказательство: PR #1478 commit 0ef4bf205960391caa95d6f75a540280abc2eb31 merge-base --is-ancestor 0ef4bf205960391caa95d6f75a540280abc2eb31 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1478 0ef4bf2
Время закрытия: 2026-11-22T11:29:08Z
