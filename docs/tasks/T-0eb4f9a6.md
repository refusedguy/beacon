---
id: T-0eb4f9a6
title: Evaluate splitting into separate CI workflows
status: available
scope:
    - .github/workflows/ci.yml
depends:
    - T-0dbf6758
priority: 52
kind: task
require_review: llm
---

Goal: Decide whether native path filters in several workflows beat one filtered workflow.

Evidence: ciopt-07 item 5. Splitting needs no third-party action but duplicates toolchain setup and shared path lists. perf-nightly hardcodes workflow_id ci.yml for its baseline, so a rename breaks the nightly comparison.

Steps: Write the comparison in the PR body and prototype only if the single-workflow filter grows unmaintainable.

Check: Required check names and nightly baseline remain valid.

Accept: Close with no change if the single workflow stays readable.
