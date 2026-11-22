// Legacy E2E fixture from the T0.3 spike, not the repository plugin.
//
// The wake logic below is copied from the working spike at
// beacon-impl/tmp/p0-wake/proj/.opencode/plugins/beacon-bridge.js (T0.3).
// The e2e suite no longer copies this file into the isolated project. The
// positive wake scenarios load .opencode/plugins/beacon-bridge.js from this
// repository, which reads events.jsonl and calls ctx.session.prompt with
// delivery steer. This fixture stays in the tree as the spike copy. For the
// negative control the suite installs fixtures/beacon-bridge-stub.js instead.
//
// Paths come from XDG_STATE_HOME so the harness can run in an isolated base.
// This file has no package.json and imports no SDK, like the spike.

import { readFileSync, writeFileSync, appendFileSync, renameSync } from "node:fs"
import { open } from "node:fs/promises"
import { homedir } from "node:os"
import { join } from "node:path"

const STATE = join(
  process.env.XDG_STATE_HOME || join(homedir(), ".local", "state"),
  "beacon",
)
const LOG = join(STATE, "plugin.log")
const EVIDENCE = join(STATE, "plugin-events.jsonl")
const EVENTS = join(STATE, "events.jsonl")
const RECEIPTS = join(STATE, "receipts")
const RECEIPT_LOG = join(STATE, "receipts.jsonl")
const CURSOR = join(STATE, "cursor.json")
const TARGET = join(STATE, "target-session.txt")

let CTX = null

function log(msg) {
  const line = `${new Date().toISOString()} ${msg}\n`
  try { appendFileSync(LOG, line) } catch {}
}

function ev(obj) {
  try { appendFileSync(EVIDENCE, JSON.stringify({ t: new Date().toISOString(), ...obj }) + "\n") } catch {}
}

function targetSession() {
  try { return readFileSync(TARGET, "utf8").trim() } catch { return "" }
}

// Atomic claim, 0600: the same id can only be delivered once (ADR 0014).
async function claim(id) {
  const path = join(RECEIPTS, `${id}.wrote`)
  try {
    const h = await open(path, "wx", 0o600)
    await h.close()
    return { ok: true, path }
  } catch (err) {
    if (err.code === "EEXIST") return { ok: false, path, eexist: true }
    throw err
  }
}

async function deliver(item) {
  const id = item.id ?? `auto-${Date.now()}`
  const sessionID = item.sessionID ?? targetSession()
  const text = item.text ?? "BEACON-E2E-WAKE"
  log(`deliver.begin id=${id} session=${sessionID}`)
  let c
  try { c = await claim(id) } catch (err) { log(`claim.error id=${id} ${err.code} ${err.message}`); return }
  if (!c.ok) { log(`claim.skip id=${id} EEXIST`); ev({ kind: "receipt-skip", id }); return }
  ev({ kind: "receipt-posted", id, state: "pending", path: c.path })
  try {
    const res = await CTX.session.prompt({ sessionID, text, delivery: "steer" })
    log(`prompt.ok id=${id} delivery=${res?.delivery ?? "?"}`)
    ev({ kind: "prompt-ok", id, sessionID, delivery: res?.delivery ?? null })
    appendFileSync(RECEIPT_LOG, JSON.stringify({ id, sessionID, state: "posted", t: new Date().toISOString() }) + "\n")
  } catch (err) {
    // fail-open: a bad wake never throws into the session (ADR 0006).
    log(`prompt.error id=${id} ${err.code ?? ""} ${String(err).slice(0, 200)}`)
    ev({ kind: "prompt-error", id, sessionID, err: String(err).slice(0, 300) })
    try { renameSync(c.path, `${c.path}.failed`) } catch {}
    appendFileSync(RECEIPT_LOG, JSON.stringify({ id, sessionID, state: "transport-failed", t: new Date().toISOString() }) + "\n")
  }
}

export default {
  id: "beacon-bridge",

  setup(ctx) {
    const ac = new AbortController()
    CTX = ctx
    log("setup version=" + ctx.app.version)
    ev({ kind: "plugin-setup", version: ctx.app.version })

    // Observe wake facts from inside the plugin.
    void (async () => {
      const types = [
        "session.execution.started", "session.execution.succeeded",
        "session.execution.failed", "session.inbox.delivered",
        "session.inbox.enqueued",
      ]
      for await (const e of ctx.event.subscribe({ types, signal: ac.signal })) {
        const d = e.data ?? {}
        ev({ kind: "ctx-event", type: e.type, sessionID: d.sessionID ?? null })
      }
    })().catch((err) => log("ctx-event watcher stopped " + err))

    // S2: poll events.jsonl every 2s and steer-prompt the target session.
    void (async () => {
      let offset = 0
      try { offset = JSON.parse(readFileSync(CURSOR, "utf8")).offset ?? 0 } catch {}
      while (!ac.signal.aborted) {
        await new Promise((r) => setTimeout(r, 2000))
        try {
          let buf = readFileSync(EVENTS, "utf8")
          if (buf.length < offset) offset = 0 // rotated
          const chunk = buf.slice(offset)
          const nl = chunk.lastIndexOf("\n")
          if (nl < 0) continue
          const complete = chunk.slice(0, nl)
          offset += nl + 1
          writeFileSync(CURSOR, JSON.stringify({ offset }))
          for (const line of complete.split("\n")) {
            if (!line.trim()) continue
            let item
            try { item = JSON.parse(line) } catch { log("bad json line skipped"); continue }
            await deliver(item)
          }
        } catch (err) {
          log(`poll.error ${err.code ?? ""} ${err.message}`)
        }
      }
    })().catch((err) => log("s2 poller stopped " + err))

    return () => { ac.abort(); log("cleanup") }
  },
}
