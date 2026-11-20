# E02_handoff_generation_stamp

## Сценарий

Исполнитель A берёт строку, ставит её blocked, затем handoff исполнителю B; B делает resolve, затем происходит следующий handoff.

## Проверки

- generation во втором in-progress соответствует B
- После второго handoff строка возвращается в pending с переходом release
- Журнал содержит оба closure_target и оба актора

## Негативный контроль

Убрать перештамповку generation при resolve: строка остаётся in-progress у мёртвого владельца, и тест должен упасть.
