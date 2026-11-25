import assert from "node:assert/strict"
import test from "node:test"

import { CONTROL_ACTIONS, BeaconStatusRpc } from "./ui_model.js"
import { createActionRunner } from "./ui_actions.js"

test("the shared vocabulary accepts every advertised verb and rejects the rest", () => {
  assert.deepEqual([...CONTROL_ACTIONS], ["approve", "unblock", "cancel", "retry", "nudge", "watch"])
  const validate = BeaconStatusRpc.methods.control.input["~standard"].validate
  for (const action of CONTROL_ACTIONS) {
    const input = action === "nudge" ? { action, runID: "run-1", message: "check the base" } : { action, runID: "run-1" }
    assert.ok(validate(input).value !== undefined, `${action} was rejected`)
  }
  assert.ok("issues" in validate({ action: "bind", runID: "run-1" }))
})

test("the action runner sends only verbs from the shared vocabulary", async () => {
  const sent = []
  const source = { control: async (input) => { sent.push(input); return { ok: true } } }
  const runAction = createActionRunner({}, source, { refresh: async () => {} })

  await runAction("watch", { id: "run-1", sha: "abc" })
  assert.deepEqual(sent, [{ action: "watch", runID: "run-1", sha: "abc" }])

  await runAction("bind", { id: "run-1" })
  assert.equal(sent.length, 1, "an unknown action reached the transport")
})
