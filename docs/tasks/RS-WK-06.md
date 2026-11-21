# RS-WK-06. Негативный контроль против дедупликации по времени
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/webhook/filters.go, internal/infra/webhook/filters_test.go
Зависимости: 
Ссылка: 
Агент: executor ses_e0377528fffegWoBVms9M3griZ
Время: 2026-11-21
Цель: Зафиксировать тестом, что events с одинаковым created, но разными ID не сливаются и повтор с тем же ID не создаёт второй эффект.
baseline: `grep -rn 'created\|Created' internal/infra/webhook/filters.go` пусто, повторено 2026-11-21. В Filter нет поля времени, значит время не может влиять на решение. Порядок проверок в decide: seenDelivery на строке 137, seenSHA на строке 140. Premise подтверждена.
Контекст: Premise: проверено 2026-11-21, дедупликация идёт по delivery и head SHA, а не по времени. baseline: `grep -rn 'created\|Created' internal/infra/webhook/filters.go` пусто. Источник: Stripe how-events-work прямо запрещает использовать created как маркер обработки.
Решение: Производственный код трогать не нужно: дедупликация уже идёт по delivery и head SHA, поля времени в API нет, порядок проверок уже delivery перед head SHA. Добавлены три негативных теста, которые фиксируют это поведение и не дают времени стать ключом дедупликации.
Шаги: Добавлены тесты в filters_test.go: TestFilterDistinctIDsAtSameCreatedDoNotMerge (разные ID и SHA при одинаковом контексте оба accept), TestFilterRepeatedIDCreatesNoSecondEffect (повтор ID даёт DecisionDuplicateDelivery и не публикует второй эффект), TestFilterDeliveryCheckedBeforeHeadSHA (при обоих совпадениях побеждает проверка delivery). Порядок проверок в filters.go не менялся.
Проверка: gofmt -w применён к filters_test.go, gofmt -l пусто. Тесты идут в CI PR. Локально go test и go build не запускались по правилам AGENTS.md. Порядок проверок в decide не изменён.
Приёмка: Совпадение времени не влияет на решение о дедупликации. Разные ID не сливаются, повтор ID не создаёт второй эффект, delivery проверяется раньше head SHA.
