---
id: T-4b1bb0a7
title: Put required checks behind a port
status: in_progress
scope:
    - pkg/core/ports.go
    - internal/infra/checksource/config.go
    - internal/infra/checksource/workflow.go
    - internal/infra/checksource/config_test.go
    - internal/infra/checksource/workflow_test.go
    - internal/infra/policy/configurable.go
    - cmd/beacon/main.go
depends:
    - T-6a81e8b7
priority: 99
kind: task
require_review: llm
---

Goal: Policy consumes required check names through an interface, not a static config slice.

Evidence: policy.Config.RequiredChecks is a plain []string copied from .beacon/config.yaml. T-6a81e8b7 adds workflow-derived names, but that still hardcodes one source. Required checks can also come from GitHub check contexts, GitLab statuses and future providers, so a provider-specific list in policy will keep drifting.

Steps: Add a RequiredCheckSource port in pkg/core with a Names(ctx) ([]string, error) contract. Implement a config source that keeps the explicit list as an override and a workflow source that parses CI files. Keep the SCM adapters free to add their own implementation later without touching policy. Wire the policy to the port in cmd/beacon and keep doctor output meaningful.

Check: A test swaps two sources and asserts the policy honours whichever names the source returns, with no policy edit. Existing required-check behaviour stays identical for the config source.

Accept: No wire shape change. Do not edit ci.yml or .beacon/config.yaml in this card.
