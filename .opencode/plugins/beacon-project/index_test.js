// Tests for the configurable shell guard in the beacon project plugin.
// Run: node --test .opencode/plugins/beacon-project/index_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  SHELL_GUARD_RULES,
  beaconProject,
  buildShellGuardRules,
  commandFromToolInput,
  createRunChangeWatcher,
  currentSessionAgent,
  findShellGuardHit,
  guardedAgents,
  guardMode,
  isWorkerAgent,
  pluginOptions,
  pollIntervalMs,
  registerShellGuard,
  resolveMcpRegistration,
  snapshotLimits,
  statusPanelEnabled,
  watcherEnabled,
} from "./index.js"
import { noopMcpRegistration, registeredBeaconConfig } from "./mcp/mcp.js"

function fakeCtx() {
  let handler = null
  let disposed = false
  return {
    tool: {
      hook: async (_name, fn) => {
        handler = fn
        return { dispose() { disposed = true } }
      },
    },
    get handler() {
      if (typeof handler !== "function") throw new Error("no hook registered")
      return handler
    },
    get disposed() {
      return disposed
    },
  }
}

async function withWarnings(fn) {
  const original = console.warn
  const messages = []
  console.warn = (...args) => messages.push(args.map(String).join(" "))
  try {
    return await fn(messages)
  } finally {
    console.warn = original
  }
}

function event(command, agent) {
  return { tool: "bash", input: { command }, agent }
}

test("commandFromToolInput reads the command from common shapes", () => {
  assert.equal(commandFromToolInput("go test ./..."), "go test ./...")
  assert.equal(commandFromToolInput({ command: "npm ci" }), "npm ci")
  assert.equal(commandFromToolInput({ args: ["rg", "-n", "x", "."] }), "rg -n x .")
  assert.equal(commandFromToolInput(null), "")
})

test("built-in heavy rules cover full builds, tests, vets, linters and installs", () => {
  const ids = SHELL_GUARD_RULES.map((rule) => rule.id)
  for (const id of [
    "full go build",
    "full go test suite",
    "full go vet",
    "full lint run",
    "npm ci",
    "npm run build",
  ]) {
    assert.ok(ids.includes(id), `missing default rule: ${id}`)
  }
  const commands = [
    "go build ./...",
    "go test ./...",
    "go vet ./...",
    "golangci-lint run ./...",
    "npm ci",
    "npm run build",
  ]
  for (const command of commands) {
    assert.ok(findShellGuardHit("bash", command), `should guard: ${command}`)
  }
})

test("a worker is blocked from a full go test suite with a message pointing to CI", async () => {
  const ctx = fakeCtx()
  await registerShellGuard(ctx, {}, {})
  await withWarnings(async () => {
    assert.throws(
      () => ctx.handler(event("go test ./...", "worker")),
      (error) => /blocked here/.test(error.message) && /CI/.test(error.message),
    )
  })
})

test("an orchestrator is allowed to run a full go test suite", async () => {
  const ctx = fakeCtx()
  await registerShellGuard(ctx, {}, {})
  await withWarnings(() => {
    assert.doesNotThrow(() => ctx.handler(event("go test ./...", "orchestrator")))
  })
})

test("warn mode logs instead of throwing", async () => {
  const ctx = fakeCtx()
  await registerShellGuard(ctx, { shellGuard: "warn" }, {})
  await withWarnings((messages) => {
    assert.doesNotThrow(() => ctx.handler(event("go test ./...", "worker")))
    assert.equal(messages.length, 1)
    assert.match(messages[0], /blocked here/)
  })
})

test("BEACON_SHELL_GUARD=warn overrides config", () => {
  assert.equal(guardMode({ shellGuard: "block" }, { BEACON_SHELL_GUARD: "warn" }), "warn")
  assert.equal(guardMode({ shellGuard: "block" }, {}), "block")
})

test("targeted commands stay allowed", async () => {
  const ctx = fakeCtx()
  await registerShellGuard(ctx, {}, {})
  const allowed = [
    "go test ./internal/app",
    "go test -run TestThing ./pkg",
    "gofmt -w file.go",
    "rg -n PATTERN ./src",
    "go build -o bin/beacon ./cmd/beacon",
    "node --test foo.test.js",
  ]
  await withWarnings(() => {
    for (const command of allowed) {
      assert.doesNotThrow(() => ctx.handler(event(command, "worker")), `should allow: ${command}`)
    }
  })
})

test("a malformed config rule is logged and skipped without breaking setup", async () => {
  const warnings = []
  const rules = buildShellGuardRules(
    {
      shellGuardRules: [
        { id: "broken", pattern: "([", replacement: "safe" },
        { id: "custom", pattern: "CUSTOM_CMD", replacement: "a safe alternative" },
      ],
    },
    (message) => warnings.push(message),
  )
  assert.ok(rules.some((rule) => rule.id === "custom"))
  assert.ok(!rules.some((rule) => rule.id === "broken"))
  assert.ok(warnings.some((message) => /invalid pattern/.test(message)))

  const ctx = fakeCtx()
  let guard
  await withWarnings((messages) => {
    return (async () => {
      guard = await registerShellGuard(ctx, {
        shellGuardRules: [{ id: "broken", pattern: "([", replacement: "safe" }],
      })
      assert.ok(messages.some((message) => /invalid pattern/.test(message)))
    })()
  })
  assert.ok(guard)
  await withWarnings(() => {
    assert.doesNotThrow(() => ctx.handler(event("harmless command", "worker")))
  })
})

test("config rules can guard new commands", async () => {
  const ctx = fakeCtx()
  await registerShellGuard(ctx, {
    shellGuardRules: [{ id: "legacy tool", pattern: "legacy-tool", replacement: "the new tool" }],
  })
  await withWarnings((messages) => {
    assert.throws(
      () => ctx.handler(event("legacy-tool --all", "worker")),
      /the new tool/,
    )
    assert.ok(messages.some((message) => /legacy tool/.test(message)))
  })
})

test("guarded agents are configurable", async () => {
  assert.deepEqual(guardedAgents({}), ["worker"])
  assert.deepEqual(guardedAgents({ shellGuardAgents: ["worker", "build"] }), ["worker", "build"])

  const ctx = fakeCtx()
  await registerShellGuard(ctx, { shellGuardAgents: ["build"] }, {})
  await withWarnings(() => {
    assert.doesNotThrow(() => ctx.handler(event("go test ./...", "worker")))
    assert.throws(() => ctx.handler(event("go test ./...", "build")), /blocked here/)
  })
})

test("plugin options merge ctx.options and ctx.config", () => {
  const ctx = { options: { statusPollMs: 3000, watcherEnabled: false }, config: { statusPanelEnabled: false } }
  const options = pluginOptions(ctx)
  assert.equal(options.statusPollMs, 3000)
  assert.equal(options.watcherEnabled, false)
  assert.equal(options.statusPanelEnabled, false)
})

test("panel and watcher options default to on and can be switched off", () => {
  assert.equal(statusPanelEnabled({}), true)
  assert.equal(watcherEnabled({}), true)
  assert.equal(statusPanelEnabled({ statusPanelEnabled: false }), false)
  assert.equal(watcherEnabled({ watcherEnabled: false }), false)
})

test("poll interval defaults to 10s and a non-positive value disables polling", () => {
  assert.equal(pollIntervalMs(undefined), 10000)
  assert.equal(pollIntervalMs(15000), 15000)
  assert.equal(pollIntervalMs(0), 0)
  assert.equal(pollIntervalMs(-1), 0)
})

test("snapshot limits are configurable with safe defaults", () => {
  assert.deepEqual(snapshotLimits({}), { recentLimit: 5, decisionsLimit: 20 })
  assert.deepEqual(snapshotLimits({ snapshotRecentLimit: 2, snapshotDecisionsLimit: 7 }), {
    recentLimit: 2, decisionsLimit: 7,
  })
  assert.deepEqual(snapshotLimits({ snapshotRecentLimit: -3 }), { recentLimit: 5, decisionsLimit: 20 })
})

test("run change watcher honours a custom interval and can be stopped", async () => {
  const timers = []
  const source = { snapshot: async () => ({ active: [], recent: [] }) }
  const watcher = createRunChangeWatcher(source, {
    emit: () => {},
    deps: {
      intervalMs: 2500,
      setInterval: (fn, ms) => { timers.push(ms); return 1 },
      clearInterval: () => {},
    },
  })
  watcher.start()
  assert.deepEqual(timers, [2500])
  assert.equal(watcher.running, true)
  watcher.stop()
  assert.equal(watcher.running, false)
})

function beaconCommandSpy(calls) {
  return (root) => { calls.push(root); return ["/bin/beacon", "mcp"] }
}

function setupCtx(agent, options = {}, root = "/setup-project") {
  return {
    location: { project: { canonical: root } },
    agent,
    options,
    session: {
      hook: async () => ({ dispose() {} }),
      list: async () => [],
    },
    tool: { hook: async () => ({ dispose() {} }) },
  }
}

test("worker agent names are recognised by substring", () => {
  assert.equal(isWorkerAgent("worker"), true)
  assert.equal(isWorkerAgent("beacon-worker"), true)
  assert.equal(isWorkerAgent("Worker"), true)
  assert.equal(isWorkerAgent("build"), false)
  assert.equal(isWorkerAgent("orchestrator"), false)
  assert.equal(isWorkerAgent(null), false)
})

test("currentSessionAgent resolves an explicit session and the newest session", async () => {
  const named = {
    sessionID: "s1",
    session: { get: async ({ sessionID }) => ({ agent: sessionID === "s1" ? "worker" : "build" }) },
  }
  assert.equal(await currentSessionAgent(named), "worker")

  const newest = { session: { list: async () => [{ id: "s2", agent: "beacon-worker" }] } }
  assert.equal(await currentSessionAgent(newest), "beacon-worker")
  assert.equal(isWorkerAgent(await currentSessionAgent(newest)), true)
})

test("a worker skips MCP config registration and command resolution", async (t) => {
  const root = "/worker-no-mcp"
  const calls = []
  const ctx = {
    agent: "beacon-worker",
    options: { mcpDeps: { beaconCommand: beaconCommandSpy(calls) } },
  }
  const registration = await resolveMcpRegistration(ctx, root)
  t.after(() => registration.dispose())

  assert.deepEqual(calls, [])
  assert.equal(registeredBeaconConfig(root), null)
  assert.equal(registration.config, null)
  assert.equal(typeof registration.dispose, "function")
})

test("build and orchestrator agents still register config and resolve the command", async (t) => {
  for (const agent of ["build", "orchestrator"]) {
    const root = `/keep-mcp-${agent}`
    const calls = []
    const ctx = {
      agent,
      options: { mcpDeps: { beaconCommand: beaconCommandSpy(calls) } },
    }
    const registration = await resolveMcpRegistration(ctx, root)
    t.after(() => registration.dispose())

    assert.deepEqual(calls, [root])
    assert.equal(registeredBeaconConfig(root), registration.config)
    assert.deepEqual(registration.config.command, ["/bin/beacon", "mcp"])
  }
})

test("worker setup skips the status panel and disposes the no-op on cleanup", async () => {
  let noop = null
  let panelRegistrations = 0
  const ctx = setupCtx("worker", { noopMcpRegistration: () => (noop = noopMcpRegistration()) }, "/worker-cleanup")
  ctx.rpc = { register: async () => { panelRegistrations += 1; return { dispose() {} } } }

  const dispose = await beaconProject.setup(ctx)
  assert.ok(noop)
  assert.equal(noop.disposed, false)
  assert.equal(panelRegistrations, 0)

  await dispose()
  assert.equal(noop.disposed, true)
})

test("build setup keeps the status panel RPC for the operator TUI", async () => {
  let panelRegistrations = 0
  const ctx = setupCtx("build", {
    statusPollMs: 0,
    watcherEnabled: false,
    mcpDeps: { beaconCommand: () => ["/bin/beacon", "mcp"] },
  }, "/build-panel")
  ctx.rpc = { register: async () => { panelRegistrations += 1; return { events: { emit() {} }, dispose() {} } } }

  const dispose = await beaconProject.setup(ctx)
  await dispose()
  assert.equal(panelRegistrations, 1)
})
