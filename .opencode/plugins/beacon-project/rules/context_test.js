// Run: node --test .opencode/plugins/beacon-project/context_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import {
  BUILD_REMINDER, ORCHESTRATOR_KEY, ORCHESTRATOR_RULES, SURVIVAL_KEY, SURVIVAL_RULES,
  ensureSurvivalRules, installProjectContext, roleFor, sessionInstructions,
} from "./context.js"

const SESSION = "ses_current"

function projectRoot(withBuildScript = false) {
  const root = mkdtempSync(join(tmpdir(), "beacon-context-"))
  if (withBuildScript) {
    mkdirSync(join(root, "scripts"))
    writeFileSync(join(root, "scripts", "build-beacon.sh"), "")
  }
  return root
}

function fakeCtx() {
  const entries = []
  const puts = []
  return {
    entries,
    puts,
    session: {
      instructions: {
        entry: {
          async list({ sessionID }) {
            assert.equal(sessionID, SESSION)
            return entries.map((item) => ({ ...item }))
          },
          async put({ sessionID, key, value }) {
            assert.equal(sessionID, SESSION)
            assert.ok(key === SURVIVAL_KEY || key === ORCHESTRATOR_KEY)
            puts.push(value)
            const found = entries.find((item) => item.key === key)
            if (found) found.value = value
            else entries.push({ key, value })
          },
        },
      },
    },
  }
}

test("ensureSurvivalRules writes once, skips unchanged text, writes again on change", async () => {
  const ctx = fakeCtx()

  assert.equal(await ensureSurvivalRules(ctx, SESSION), true)
  assert.equal(ctx.puts.length, 1)
  assert.equal(ctx.puts[0], SURVIVAL_RULES)

  assert.equal(await ensureSurvivalRules(ctx, SESSION), false)
  assert.equal(ctx.puts.length, 1)

  ctx.entries[0].value = "changed rules"
  assert.equal(await ensureSurvivalRules(ctx, SESSION), true)
  assert.equal(ctx.puts.length, 2)
  assert.equal(ctx.puts[1], SURVIVAL_RULES)
})

test("installProjectContext writes rules only for the current session", async () => {
  const ctx = fakeCtx()
  const callbacks = {}
  ctx.session.hook = async (name, callback) => {
    callbacks[name] = callback
    return { dispose() {} }
  }

  await installProjectContext(ctx, "/project")

  const event = { sessionID: SESSION, agent: "build", system: [], prompt: {} }
  await callbacks.context(event)
  await callbacks.compaction(event)
  await callbacks.context(event)

  assert.equal(ctx.puts.length, 1)
  assert.equal(ctx.puts[0], ORCHESTRATOR_RULES)
  assert.equal(event.system[0].text, "beacon project root=/project")
})

test("worker rules list the real Beacon tools", async () => {
  for (const name of [
    "beacon_dispatch", "beacon_watch_pr", "beacon_status", "beacon_recent",
    "beacon_wait", "beacon_control", "beacon_task_create", "beacon_task_delete",
    "beacon_task_update", "beacon_reconcile",
  ]) {
    assert.ok(SURVIVAL_RULES.includes(name), `worker rules missing ${name}`)
  }
  assert.ok(!SURVIVAL_RULES.includes("The sixth tool"))
})

test("orchestrator sessions get orchestrator rules instead of worker rules", async () => {
  const ctx = fakeCtx()
  const wanted = await sessionInstructions(ctx, { sessionID: SESSION, agent: "orchestrator" })
  assert.equal(wanted.key, ORCHESTRATOR_KEY)
  assert.equal(wanted.value, ORCHESTRATOR_RULES)
  assert.ok(wanted.value.includes("Scope:"))
})

test("session.get fallback resolves the agent when the event omits it", async () => {
  const ctx = fakeCtx()
  ctx.session.get = async () => ({ data: { agent: "worker" } })
  const wanted = await sessionInstructions(ctx, { sessionID: SESSION })
  assert.equal(wanted.key, SURVIVAL_KEY)
  assert.equal(wanted.value, SURVIVAL_RULES)
})

test("build is the operator's orchestration agent, not a worker", async () => {
  assert.equal(roleFor("build"), "orchestrator")
  const ctx = fakeCtx()
  const wanted = await sessionInstructions(ctx, { sessionID: SESSION, agent: "build" })
  assert.equal(wanted.key, ORCHESTRATOR_KEY)
  assert.equal(wanted.value, ORCHESTRATOR_RULES)
})

test("read-only and unknown agents get no commit or card rules", async () => {
  assert.equal(roleFor("issue-scout"), "none")
  assert.equal(roleFor("explore"), null)
  assert.equal(roleFor("networker"), null, "a substring match never makes a worker")
  assert.equal(roleFor(null), null)
  const ctx = fakeCtx()
  assert.equal(await sessionInstructions(ctx, { sessionID: SESSION, agent: "issue-scout" }), null)
  assert.equal(await sessionInstructions(ctx, { sessionID: SESSION, agent: "networker" }), null)
  assert.equal(await sessionInstructions(ctx, { sessionID: SESSION, agent: "unknown" }), null)
  assert.equal(await sessionInstructions(ctx, { sessionID: SESSION }), null)
})

test("orchestrator rules warn against duplicate cards and dispatch loops", () => {
  for (const text of [
    "git log -n 10 --oneline", "beacon_recent", "narrow, disjoint Scope",
    "Never dispatch blindly",
  ]) {
    assert.ok(ORCHESTRATOR_RULES.includes(text), `orchestrator rules missing ${text}`)
  }
  assert.ok(!ORCHESTRATOR_RULES.includes("scripts/build-beacon.sh"), "the build reminder is project-specific")
})

test("worker rules are formatter and package agnostic", () => {
  for (const text of ["gofmt", "go test", "go vet", "npm ci"]) {
    assert.ok(!SURVIVAL_RULES.includes(text), `worker rules still assume a stack: ${text}`)
  }
  assert.ok(SURVIVAL_RULES.includes("project formatter"))
  assert.ok(SURVIVAL_RULES.includes("targeted tests for changed packages"))
  assert.ok(SURVIVAL_RULES.includes("repo-wide linters to CI"))
})

test("an ordinary project gets no build reminder", async (t) => {
  const root = projectRoot(false)
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const ctx = fakeCtx()
  const wanted = await sessionInstructions(ctx, { sessionID: SESSION, agent: "orchestrator" }, {}, root)
  assert.equal(wanted.value, ORCHESTRATOR_RULES)
  assert.ok(!wanted.value.includes(BUILD_REMINDER))
})

test("a Beacon repo gets the conditional build reminder", async (t) => {
  const root = projectRoot(true)
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const ctx = fakeCtx()
  const wanted = await sessionInstructions(ctx, { sessionID: SESSION, agent: "orchestrator" }, {}, root)
  assert.equal(wanted.value, `${ORCHESTRATOR_RULES}\n${BUILD_REMINDER}`)
  assert.ok(wanted.value.includes("scripts/build-beacon.sh"))
  assert.ok(wanted.value.includes("restart the MCP daemon"))

  const worker = await sessionInstructions(ctx, { sessionID: SESSION, agent: "worker" }, {}, root)
  assert.equal(worker.value, SURVIVAL_RULES, "workers never get the build reminder")
})

test("installProjectContext passes the root to the compaction hook too", async (t) => {
  const root = projectRoot(true)
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const ctx = fakeCtx()
  const callbacks = {}
  ctx.session.hook = async (name, callback) => {
    callbacks[name] = callback
    return { dispose() {} }
  }
  await installProjectContext(ctx, root)
  const event = { sessionID: SESSION, agent: "build", system: [] }
  await callbacks.compaction(event)
  assert.equal(ctx.puts[0], `${ORCHESTRATOR_RULES}\n${BUILD_REMINDER}`)
})

test("orchestrator rules ask for status checks before acting on a complaint", () => {
  for (const text of ["beacon_status", "beacon status --daemon", "git status --short", "no ready tasks"]) {
    assert.ok(ORCHESTRATOR_RULES.includes(text), `orchestrator rules missing ${text}`)
  }
})

test("survivalRulesEnabled false disables role instruction injection", async () => {
  const ctx = fakeCtx()
  ctx.session.get = async () => ({ data: { agent: "orchestrator" } })
  const wanted = await sessionInstructions(ctx, { sessionID: SESSION }, { survivalRulesEnabled: false })
  assert.equal(wanted, null)
})
