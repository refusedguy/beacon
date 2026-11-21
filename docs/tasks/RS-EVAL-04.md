# RS-EVAL-04. Property based тесты контракта beacon-gates
Статус: done
Фаза: P3
Тип: task
Владелец файлов: tools/beacon-gates/gate/fixtures.go, tools/beacon-gates/gate/gate_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
baseline: `grep -c '^func Test' tools/beacon-gates/gate/gate_test.go` = 6 на origin/main d3b1bfb; после изменений = 15.
Решение: Девять property-тестов в gate_test.go. Мастер-сид 20261121 даёт 128 детерминированных прогонов на свойство. Сиды из tools/beacon-gates/gate/testdata/seeds.txt реплеятся первыми, а упавший сид дописывается в файл автоматически. ParseOptions проверяет round-trip четырёх форм флагов и отказ некорректных аргументов. SortFindings проверяет идемпотентность, сохранение всех finding и порядок по тройке Path, Line, Rule без участия Message. Finish проверяет, что пустой список с порогом больше нуля всегда возвращает Red и добавляет SCAN-BLIND. FieldValue проверяет обрезку пробелов, номер строки и первый матч. CheckLinks проверяет, что ссылка на существующий файл или якорь не даёт finding, а отсутствующий файл или якорь даёт DOC-LINK-PATH или DOC-LINK-ANCHOR.
Граница: Поведение Finish и правило SCAN-BLIND не изменены. Новые тесты не трогают продакшн-код гейта.
Цель: Проверять целый класс входов: ParseOptions, SortFindings, Finish, FieldValue и CheckLinks, с сохранением упавшего seed и воспроизведением в CI.
Контекст: Premise: проверено 2026-11-21, контракт зафиксирован таблицами из нескольких примеров. baseline: `grep -c '^func Test' tools/beacon-gates/gate/gate_test.go` = 6. Источник: Hypothesis stateful tests и replay failures.
Шаги: Добавить генератор с фиксированным seed. Свойства: SortFindings идемпотентен и строг по тройке Path, Line, Rule; Finish с minFindings больше нуля на пустом списке никогда не даёт Green; ссылка на существующий файл не даёт finding. Упавший seed писать в testdata/seeds.txt.
Проверка: gofmt -w применён и gofmt -l пуст. Локально go test не запускался по правилам репозитория, проверка идёт в CI на PR. Случайные значения в новых тестах идут только от зафиксированного сида 20261121.
Исправление по CI: первый прогон нашёл panic в TestPropertyCheckLinks на ветке с отсутствующим файлом, карта фикстур была nil. Карта создаётся до switch, остальные property-тесты в том прогоне прошли.
Приёмка: Упавший пример воспроизводится по сиду без ручного разбора.
