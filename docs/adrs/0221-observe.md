# ADR 221: observe как нижний слой матрицы архитектурных гейтов

Статус: proposed

## Контекст

На main (916f1b4) layers-ast даёт 5 finding LAYER-FORBIDDEN-EDGE на импорты internal/infra/observe. separate строки observe в tools/beacon-gates/gate/config/layers.json нет, поэтому по longest-prefix он попадает в internal/infra (rank 1), и импорт из internal/infra/cibridge, ghci, mcp, wake разбирается как ребро internal/infra -> internal/infra, которого нет в allowed. Матрица не менялась с коммитов c1463d1 и 6a29da8, а хуки добавили 37cd38b и 99fd0ac.

## Решение

Добавить в layers.json строку {"name":"internal/infra/observe","rank":0} и рёбра internal/infra -> internal/infra/observe, internal/app -> internal/infra/observe, cmd -> internal/infra/observe. Ранг 0 обязателен: gate/layers.go:74-76 отклоняет to.Rank >= from.Rank. Обязательна пересборка закоммиченного бинарника точными флагами из gates-binary.yml.

## Последствия

Проверено на копии: layers-ast, layers-list и capabilities становятся зелёными. observe остаётся физически в internal/infra, поэтому capabilities-гейт не ломается. Четыре исходных разрешённых ребра остаются используемыми, LAYER-UNUSED-RULE не возникает. Требуется коммит tools/beacon-gates/beacon-gates: без пересборки конфиг вшит через go:embed и не влияет на результат.
