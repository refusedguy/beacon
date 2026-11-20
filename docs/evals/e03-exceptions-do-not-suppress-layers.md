# E03_exceptions_do_not_suppress_layers

## Сценарий

В копии добавить пять permanent-записей с rule LAYER-FORBIDDEN-EDGE на пути нарушающих файлов, пересобрать бинарник, запустить layers-ast.

## Проверки

- exit=1
- те же 5 finding
- layers gates не вызывают LoadExceptions

## Негативный контроль

capabilities.go:166 действительно вызывает LoadExceptions, то есть исключения работают только для capabilities-гейта.
