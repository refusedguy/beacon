---
id: T-ea8169ba
title: Pin plugin TUI peers to the supported CLI
status: done
scope:
    - .opencode/plugins/beacon-project/package.json
    - .opencode/plugins/beacon-project/supported-cli.json
    - .opencode/plugins/beacon-project/peers_test.js
depends:
    - T-e3a8c320
priority: 90
kind: task
require_review: llm
---

Goal: Peer versions match the OpenTUI versions bundled by the supported OpenCode CLI, and no TUI peer is a runtime dependency.

Evidence: Both failure modes come from peer resolution drifting between host runtime, local node_modules, and open version ranges. OpenCode 2.4.2 bundles OpenTUI 0.9.9; CLI 2.3.2 bundled 0.8.4.

Steps: Add supported-cli.json recording the CLI version and exact peer versions. Change package.json peerDependencies to those exact versions. Keep @opentui/* and solid-js out of dependencies. Add peers_test.js that reads the manifest and fails on a range, a wrong version, or a TUI peer in dependencies.

Check: node --test .opencode/plugins/beacon-project/peers_test.js passes on main and fails after any manifest or package drift.

Accept: No Go code and no wire change.

Open a PR, call beacon_watch_pr, do not self-merge.
