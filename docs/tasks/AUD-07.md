# AUD-07. Привести ID и статусы taskcli к контракту taskstore
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/taskcli/service.go, internal/app/taskcli/service_test.go, cmd/beacon/task.go, cmd/beacon/task_test.go
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Использовать единый контракт taskstore.IsID и IsValidStatus во всех парсерах taskcli и cmd.
Контекст: Источник: аудит fix/c18-a17, P1.5. idrRE в service.go отклоняет ATX-R01 и RS-CMP-06. List не принимает --status blocked и --status duplicate. cmd/beacon/task.go whyCardID и whyClosed не видят ATX-R01 и не считают duplicate закрытой карточкой. baseline: 4 расхождения после PR #1423.
Шаги: Заменить локальные regex и списки статусов на taskstore.IsID и taskstore.IsValidStatus. Привести whyCardID и whyClosed к тому же контракту, duplicate считать закрытым. Добавить тесты на ATX-R01, RS-CMP-06, blocked и duplicate.
Проверка: CI зелёный. Тесты принимают новые ID и статусы во всех четырёх точках. Нет дублирования списков ID и статусов в taskcli и cmd.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.

Итог: Already landed in #1477 (63891e1f946fea6878a1afffb9633a34276c577e). No duplicate code needed.
Доказательство: PR #1477 commit 63891e1f946fea6878a1afffb9633a34276c577e merge-base --is-ancestor 63891e1f946fea6878a1afffb9633a34276c577e origin/main exit 0; no open duplicate PR.
Причина: already landed in #1477 63891e1
Время закрытия: 2026-11-22T11:29:08Z
