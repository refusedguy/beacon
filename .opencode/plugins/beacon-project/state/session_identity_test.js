// Run: node --test .opencode/plugins/beacon-project/session_identity_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { agentRole, cliEnv, guardedAgents, sessionIdentity } from "./session_identity.js"
import { roleFor } from "../rules/context.js"
import { isWorkerAgent } from "../shell_guard.js"
import { resolveMcpRegistration } from "../index.js"
import { CliStatusSource, createStatusSource, errorMessage } from "../ui/ui.js"

test("cliEnv carries the project root, session and model into the child env", () => {
  const env = cliEnv("/p", { sessionID: "ses_123", model: "hy4-flash-free", env: { PATH: "/bin" } })
  assert.equal(env.PATH, "/bin")
  assert.equal(env.BEACON_PROJECT_ROOT, "/p")
  assert.equal(env.BEACON_PARENT_SESSION, "ses_123")
  assert.equal(env.BEACON_MODEL, "hy4-flash-free")
})

test("cliEnv omits session and model when the context cannot supply them", () => {
  const env = cliEnv("/p", { env: {} })
  assert.deepEqual(env, { BEACON_PROJECT_ROOT: "/p" })
  assert.equal("BEACON_PARENT_SESSION" in env, false)
  assert.equal("BEACON_MODEL" in env, false)
})

test("sessionIdentity reads the current session and its model from the session domain", async () => {
  const ctx = {
    sessionID: "ses_1",
    session: {
      get: async ({ sessionID }) => {
        assert.equal(sessionID, "ses_1")
        return { data: { model: { id: "hy4-flash-free", providerID: "opencode" } } }
      },
    },
  }
  assert.deepEqual(await sessionIdentity(ctx), { sessionID: "ses_1", model: "hy4-flash-free" })
})

test("sessionIdentity falls back to the newest session and accepts modelID", async () => {
  const ctx = {
    session: {
      list: async (query) => {
        assert.deepEqual(query, { order: "desc", limit: 1 })
        return [{ id: "ses_new", model: { modelID: "gpt-5" } }]
      },
      get: async () => ({ model: { modelID: "gpt-5" } }),
    },
  }
  assert.deepEqual(await sessionIdentity(ctx), { sessionID: "ses_new", model: "gpt-5" })
})

test("sessionIdentity returns no values without a session and survives a failing get", async () => {
  assert.deepEqual(await sessionIdentity(null), {})
  assert.deepEqual(await sessionIdentity({}), {})
  const failing = {
    sessionID: "ses_2",
    session: { get: async () => { throw new Error("session unavailable") } },
  }
  assert.deepEqual(await sessionIdentity(failing), { sessionID: "ses_2" })
})

const AGENT_CLASSIFICATIONS = [
  ["worker", "worker", true],
  ["build", "orchestrator", false],
  ["orchestrator", "orchestrator", false],
  ["networker", null, false],
  ["issue-scout", "none", false],
]

test("one classifier decides the role for rules, guard and MCP", async () => {
  for (const [agent, expectedRole, expectedWorker] of AGENT_CLASSIFICATIONS) {
    assert.equal(agentRole(agent), expectedRole, `agentRole(${agent})`)
    assert.equal(roleFor(agent), expectedRole, `roleFor(${agent}) must match agentRole`)
    assert.equal(isWorkerAgent(agent), expectedWorker, `isWorkerAgent(${agent}) must match role`)

    const calls = []
    const registrar = {
      registerBeaconMcp: async () => { calls.push(agent); return { dispose() {} } },
    }
    const registration = await resolveMcpRegistration({}, "/p", { agent, registrar })
    await registration.dispose()
    assert.equal(registration.config === null, expectedWorker, `MCP worker decision for ${agent}`)
    assert.equal(calls.length, expectedWorker ? 0 : 1, `MCP registration calls for ${agent}`)
  }
})

test("worker names match exactly and a configured name overrides the default", () => {
  assert.deepEqual(guardedAgents({}), ["worker"])
  assert.deepEqual(guardedAgents({ shellGuardAgents: ["worker", "beacon-worker"] }), ["worker", "beacon-worker"])
  assert.equal(agentRole("networker"), null, "a substring never makes a worker")
  assert.equal(agentRole("networker", { shellGuardAgents: ["beacon-worker"] }), null)
  assert.equal(agentRole("beacon-worker", { shellGuardAgents: ["beacon-worker"] }), "worker")
  assert.equal(agentRole(" Worker "), "worker")
  assert.equal(agentRole(null), null)
})

const source = (opts, execFile) => new CliStatusSource({
  root: "/p", ...opts,
  deps: { command: () => ["/bin/beacon"], execFile, existsSync: () => false },
})

test("CLI source passes the parent session and model to every call", async () => {
  const envs = []
  const cli = source({ sessionID: "ses_op", model: "hy4-flash-free" }, async (program, args, opts) => {
    envs.push(opts.env)
    return { stdout: '{"started":1,"active":[],"recent":[]}' }
  })
  await cli.dispatch({ n: 1 })
  await cli.snapshot()
  await cli.control({ action: "approve", runID: "run-1" })
  assert.equal(envs.length, 3)
  for (const env of envs) {
    assert.equal(env.BEACON_PROJECT_ROOT, "/p")
    assert.equal(env.BEACON_PARENT_SESSION, "ses_op")
    assert.equal(env.BEACON_MODEL, "hy4-flash-free")
  }
})

test("a missing session ID keeps snapshot and control working", async () => {
  const calls = []
  const cli = source({}, async (program, args, opts) => {
    calls.push({ args, env: opts.env })
    return { stdout: '{"active":[],"recent":[]}' }
  })
  await cli.snapshot()
  await cli.control({ action: "retry", runID: "run-1" })
  assert.equal("BEACON_PARENT_SESSION" in calls[0].env, false)
  assert.equal("BEACON_MODEL" in calls[0].env, false)
  assert.equal(calls[1].args.includes("--run-id"), true)
})

test("the parent session ID stays out of dispatch args and error text", async () => {
  const sessionID = "ses_private_123"
  let args, env
  const cli = source({ sessionID }, async (program, argv, opts) => {
    args = argv; env = opts.env
    throw new Error("dispatch refused")
  })
  await assert.rejects(() => cli.dispatch(), (error) => !errorMessage(error).includes(sessionID))
  assert.equal(args.some((arg) => arg.includes(sessionID)), false)
  assert.equal(env.BEACON_PARENT_SESSION, sessionID)
})

test("createStatusSource forwards identity to its CLI fallback", async () => {
  let env
  const deps = {
    command: () => ["/bin/beacon"],
    execFile: async (program, args, opts) => { env = opts.env; return { stdout: '{"active":[],"recent":[]}' } },
    existsSync: () => false,
  }
  const cli = createStatusSource({}, { root: "/p", deps, sessionID: "ses_fb", model: "m1" })
  await cli.snapshot()
  assert.equal(env.BEACON_PARENT_SESSION, "ses_fb")
  assert.equal(env.BEACON_MODEL, "m1")
})
