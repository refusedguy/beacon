# BVF-03. Устранить пересечения owner_files внутри волн пакета bcx.json
Статус: done
Фаза: P2
Тип: task
Владелец файлов: docs/research/batches/bcx.json
Зависимости: 
Ссылка: 
Агент: ses_dfcdeee94ffedBAihWnoJcOwwG
Время: 2026-11-22T12:01:47Z
Цель: Развести 44 пересечения owner_files между карточками одной фазы в пакете bcx.json.
Контекст: Отчёт docs/research/BATCH-VALIDATION.md. Пересечения распределены по фазам: P0 14, P1 22, P2 8. Чаще всего повторяются cmd/beacon/main.go, internal/infra/mcp/schema.go, internal/domain/types.go, internal/infra/mcp/contract_test.go и docs/adrs/README.md.
Шаги: Сгруппировать карточки по файлам и фазам. Для каждой группы оставить один владелец-файл у одной карточки, остальным назначить другой файл, перенести фазу или добавить deps. Не изменять чужие PR.
Проверка: Проверка пересечений owner_files по bcx.json для каждой фазы возвращает пустой результат.
Приёмка: Нет ни одного файла, закреплённого за двумя карточками bcx с одинаковым phase.

Причина: Требование уже в main. PR #1458, полный SHA 7e99886d4af4dbf65b8e8907d28b41972bd261df. git merge-base --is-ancestor 7e99886d origin/main вернул 0. Аудит в docs/research/batches/bcx-fix-report.md:61: cards=82 missing=0 directory_refs=0 overlap_groups=0.
Закрыл: ses_dfcdeee94ffedBAihWnoJcOwwG
Время закрытия: 2026-11-22T12:02:09Z