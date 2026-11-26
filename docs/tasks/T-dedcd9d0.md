---
id: T-dedcd9d0
title: Delete one-use aliases and template parameter
status: available
scope:
    - cmd/beacon/serve.go
    - cmd/beacon/config_watch.go
    - internal/infra/harness/cli_exec.go
priority: 72
kind: task
require_review: llm
---

Goal: Remove internal indirection that adds no behaviour.

Evidence: quality-07 items 1 and 2. idleWatcher aliases core.IdleWatcher for one assertion. render(tmpl, prompt) receives the receiver's own Template from its only caller.

Steps: Use core.IdleWatcher directly and read c.Template inside render. Delete the alias and parameter.

Check: cmd/beacon and harness tests pass.
