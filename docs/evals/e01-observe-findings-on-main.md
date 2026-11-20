# E01_observe_findings_on_main

## Сценарий

Запустить закоммиченный tools/beacon-gates/beacon-gates layers-ast на копии чистого main 916f1b4.

## Проверки

- exit=1
- ровно 5 finding LAYER-FORBIDDEN-EDGE
- cibridge/bridge.go:16, ghci/provider.go:13, mcp/tool_get_result.go:26, mcp/tool_watch_pr.go:16, wake/deliver.go:30
- capabilities на том же дереве green

## Негативный контроль

Тот же бинарник на git archive 68cd910 (родителе 37cd38b) печатает green: no findings.
