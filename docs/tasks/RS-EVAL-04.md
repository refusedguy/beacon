# RS-EVAL-04. Property based тесты контракта beacon-gates
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: tools/beacon-gates/gate/fixtures.go, tools/beacon-gates/gate/gate_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Проверять целый класс входов: ParseOptions, SortFindings, Finish, FieldValue и CheckLinks, с сохранением упавшего seed и воспроизведением в CI.
Контекст: Premise: проверено 2026-11-21, контракт зафиксирован таблицами из нескольких примеров. baseline: `grep -c '^func Test' tools/beacon-gates/gate/gate_test.go` = 6. Источник: Hypothesis stateful tests и replay failures.
Шаги: Добавить генератор с фиксированным seed. Свойства: SortFindings идемпотентен и строг по тройке Path, Line, Rule; Finish с minFindings больше нуля на пустом списке никогда не даёт Green; ссылка на существующий файл не даёт finding. Упавший seed писать в testdata/seeds.txt.
Проверка: gofmt чистый. Тесты в CI. Случайные значения больше не встречаются в тестах гейта без фиксированного seed.
Приёмка: Упавший пример воспроизводится по сиду без ручного разбора.
