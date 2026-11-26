---
id: T-c96f8da4
title: Cache the OpenCode CLI in plugin smoke
status: available
scope:
    - .github/workflows/ci.yml
depends:
    - T-259256fd
priority: 64
kind: task
require_review: llm
---

Goal: Download the CLI only when supported-cli.json changes.

Evidence: final-09. plugin-tui-smoke downloads the CLI on every run.

Steps: Cache the installed binary keyed on a hash of supported-cli.json.

Check: A repeat run with an unchanged file restores the cache and skips the download.

Accept: A version change must invalidate the key.
