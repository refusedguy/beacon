---
id: T-fa54c87d
title: Align config defaults and expose orphan and model timeouts
status: done
scope:
    - internal/infra/config/config.go
    - internal/app/reconciler.go
    - cmd/beacon/main.go
    - cmd/beacon/init.go
    - cmd/beacon/init_config.go
    - README.md
priority: 5
kind: task
require_review: llm
---

Goal: align config defaults and expose the two settings that have silent failure modes. Discussion report: ApplyDefaults uses max_in_flight=20 and leaves max_active_agents=0 (dispatcher.go:665 falls back to in-flight), defaults() uses 20 and 4, beacon init flags use 10 and 2, and .beacon/config.yaml uses 10 and 5. An operator sees one value and gets another depending on how config was created.

Steps:
1. Make all four default sources agree. Pick one canonical default set in internal/infra/config/config.go and have ApplyDefaults, defaults() and beacon init flags read it. Add a test asserting no divergence.
2. Expose orphan timeout. defaultOrphanTimeout is 90s at internal/app/reconciler.go:14 and the Reconciler struct already has OrphanTimeout at :96, but newReconciler at cmd/beacon/main.go:368 never assigns it. Add a config field and copy it in. Users with slow PR creation can raise it instead of losing a live session; users with dead sessions must understand that slots stay occupied.
3. Expose the model resolution timeout. resolveModel hard-codes 3s at cmd/beacon/main.go:447; on timeout it returns a zero ModelRef and workers inherit the server default silently. Add a config field with the current value as default and log the timeout with the resolved provider.
4. Fix the provider ID bug at cmd/beacon/main.go:438 and :441: resolveModel hard-codes ProviderID 'opencode' for config and environment paths while the parent-session lookup returns the provider from the server. Use the resolved provider consistently.
5. Add a doctor or config validation warning when BEACON_MODEL overrides Harness.Model, and document precedence in README.md. Do not remove the environment variables because the pooled MCP daemon uses BEACON_PROJECT_ROOT.

Accept: one canonical set of concurrency defaults, an operator can tune orphan and model timeouts, and a model override is visible in config or logs instead of failing silently.
