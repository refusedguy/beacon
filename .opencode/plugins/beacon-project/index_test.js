// Tests for the configurable shell guard in the beacon project plugin.
// Run: node --test .opencode/plugins/beacon-project/index_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import {
  SHELL_GUARD_RULES,
  buildShellGuardRules,
  commandFromToolInput,
  findShellGuardHit,
  guardedAgents,
  guardMode,
  registerShellGuard,
} from "./index.js"

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
