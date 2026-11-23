// Run: node --test .opencode/plugins/beacon-project/context_test.js

import { test } from "node:test"
import assert from "node:assert/strict"
import { SURVIVAL_KEY, SURVIVAL_RULES, ensureSurvivalRules, installProjectContext } from "./context.js"

const SESSION = "ses_current"

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
            assert.equal(key, SURVIVAL_KEY)
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

  const event = { sessionID: SESSION, system: [], prompt: {} }
  await callbacks.context(event)
  await callbacks.compaction(event)
  await callbacks.context(event)

  assert.equal(ctx.puts.length, 1)
  assert.equal(event.system[0].text, "beacon project root=/project")
})
