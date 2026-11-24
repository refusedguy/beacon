// The operator RPC registered by index.js must carry nudge guidance through
// to the CLI control call. Run: node --test .opencode/plugins/beacon-project/*_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { beaconProject } from "./index.js"
import { BeaconStatusRpc, CliStatusSource } from "./ui.js"

test("the registered status RPC forwards nudge and message to CLI control", async (t) => {
  const captured = []
  const original = CliStatusSource.prototype.control
  CliStatusSource.prototype.control = async function (input) {
    captured.push(input)
    return { ok: true }
  }
  t.after(() => { CliStatusSource.prototype.control = original })

  let schema = null
  let handlers = null
  const ctx = {
    location: { project: { canonical: "/nudge-rpc" } },
    agent: "build",
    options: {
      statusPollMs: 0, watcherEnabled: false,
      mcpDeps: { beaconCommand: () => ["/bin/beacon", "mcp"] },
    },
    session: { hook: async () => ({ dispose() {} }), list: async () => [] },
    tool: { hook: async () => ({ dispose() {} }) },
    rpc: {
      register: async (definition, implementation) => {
        schema = definition
        handlers = implementation
        return { events: { emit() {} }, dispose() {} }
      },
    },
  }
  const dispose = await beaconProject.setup(ctx)
  assert.equal(schema, BeaconStatusRpc)
  assert.equal(typeof handlers.control, "function")

  const input = { action: "nudge", runID: "run-1", message: "check the base branch" }
  assert.ok(schema.methods.control.input["~standard"].validate(input).value !== undefined)
  assert.deepEqual(await handlers.control(input), { ok: true })
  assert.deepEqual(captured, [input])
  await dispose()
})
