# E02_observe_matrix_fix

## Сценарий

В копии добавить слой internal/infra/observe rank 0 и рёбра infra/app/cmd -> observe, пересобрать бинарник и прогнать layers-ast, layers-list, capabilities.

## Проверки

- все три гейта exit=0
- нет LAYER-UNUSED-RULE
- старые четыре разрешённых ребра остались использованными

## Негативный контроль

Та же правка layers.json без пересборки бинарника сохраняет 5 finding, потому что конфиг вшит через go:embed.
