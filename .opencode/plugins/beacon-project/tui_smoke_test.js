// CI guard for the real OpenCode TUI boot. Unit tests cannot see the two
// failures that only appear at startup: "Cannot find package" from a dynamic
// peer import and "No renderer found" from a second Solid/OpenTUI copy. This
// boots the CLI pinned in supported-cli.json under a pseudo-terminal with the
// repository as cwd and waits for the Beacon sidebar panel.
//
// Fast checks: node --test .opencode/plugins/beacon-project/tui_smoke_test.js
// Live boot:   bash scripts/plugin-tui-smoke.sh (runs this file with --live)

import { test } from "node:test"
import assert from "node:assert/strict"
import { execFileSync, spawn, spawnSync } from "node:child_process"
import { readFileSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const HERE = dirname(fileURLToPath(import.meta.url))
export const REPO_ROOT = resolve(HERE, "..", "..", "..")
export const MANIFEST = join(HERE, "supported-cli.json")

// The panel header text in ui/ui_sidebar.js. The failure strings are the
// exact messages seen in operator logs and in OpenTUI when two copies load.
export const READY_MARKER = "Beacon"
export const FAILURE_MARKERS = Object.freeze([
  "No renderer found",
  "Cannot find package",
  "different core runtime module",
  "failed to load plugin",
])
export const DEFAULT_TIMEOUT_MS = 25_000
const DEFAULT_POLL_MS = 100

export function readCliVersion(read = readFileSync, path = MANIFEST) {
  const manifest = JSON.parse(read(path, "utf8"))
  const version = manifest?.cliVersion
  if (typeof version !== "string" || !/^\d+\.\d+\.\d+$/.test(version)) {
    throw new Error(`supported-cli.json has no exact cliVersion, got ${JSON.stringify(version)}`)
  }
  return version
}

export function classifyOutput(output) {
  const text = String(output ?? "")
  for (const marker of FAILURE_MARKERS) {
    if (text.includes(marker)) {
      return { kind: "error", marker, reason: `plugin load error: ${marker}` }
    }
  }
  if (text.includes(READY_MARKER)) {
    return { kind: "ready", reason: "Beacon sidebar panel visible" }
  }
  return { kind: "pending", reason: "Beacon sidebar panel not seen yet" }
}

export function findPtyHelper(commandExists) {
  if (commandExists("script")) return "script"
  return null
}

export function buildRunCommand({ helper }) {
  if (helper !== "script") throw new Error(`unsupported PTY helper: ${helper}`)
  // stty gives the TUI a renderable size when CI has no window size set.
  // --standalone keeps the boot off any shared daemon the operator may run.
  const cli = "stty rows 40 cols 120; opencode --standalone"
  return { command: "script", args: ["-q", "-e", "-f", "-c", cli, "/dev/null"] }
}

function shellCommandExists(command) {
  return spawnSync("sh", ["-c", `command -v ${command} >/dev/null 2>&1`]).status === 0
}

function installedVersion() {
  return execFileSync("opencode", ["--version"], { encoding: "utf8" })
}

export async function runLive(deps = {}) {
  const {
    spawnFn = spawn,
    commandExists = shellCommandExists,
    readInstalledVersion = installedVersion,
    kill = (pid, signal) => process.kill(pid, signal),
    read = readFileSync,
    manifestPath = MANIFEST,
    cwd = REPO_ROOT,
    timeoutMs = DEFAULT_TIMEOUT_MS,
    pollMs = DEFAULT_POLL_MS,
    log = console.log,
    env = { ...process.env, CI: "1", TERM: "xterm-256color" },
  } = deps

  const version = readCliVersion(read, manifestPath)
  const helper = findPtyHelper(commandExists)
  if (!helper) {
    log("SKIP: no pseudo-terminal helper (need `script` from util-linux); Beacon TUI smoke test skipped")
    return 0
  }

  // A different CLI bundles different OpenTUI versions and invalidates the
  // pin, so refuse to boot an unknown binary.
  let found
  try {
    found = readInstalledVersion()
  } catch (error) {
    log(`Beacon TUI smoke: FAIL (cannot read opencode --version: ${error.message})`)
    return 1
  }
  if (!String(found).includes(version)) {
    log(`Beacon TUI smoke: FAIL (installed CLI is ${String(found).trim()}, expected ${version})`)
    return 1
  }

  const { command, args } = buildRunCommand({ helper })
  log(`Beacon TUI smoke: opencode@${version} under ${helper}, cwd ${cwd}, timeout ${timeoutMs}ms`)

  return new Promise((resolve) => {
    let output = ""
    let settled = false
    let poll
    let timer
    let child
    try {
      child = spawnFn(command, args, {
        cwd, env, stdio: ["ignore", "pipe", "pipe"], detached: true,
      })
    } catch (error) {
      log(`Beacon TUI smoke: FAIL (could not start ${command}: ${error.message})`)
      resolve(1)
      return
    }
    const collect = (chunk) => { output += chunk.toString("utf8") }
    child.stdout?.on("data", collect)
    child.stderr?.on("data", collect)

    const finish = (result, fallback) => {
      if (settled) return
      settled = true
      clearInterval(poll)
      clearTimeout(timer)
      // detached puts script and its CLI child in one process group.
      if (child.pid) {
        try { kill(-child.pid, "SIGTERM") } catch { /* already gone */ }
      }
      const final = result.kind === "pending" ? { kind: "error", reason: fallback } : result
      if (final.kind === "ready") {
        log(`Beacon TUI smoke: PASS (${final.reason})`)
        resolve(0)
        return
      }
      log(`Beacon TUI smoke: FAIL (${final.reason})`)
      const tail = output.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "").trim().split("\n").slice(-12).join("\n")
      if (tail) log(`--- TUI output tail ---\n${tail}\n--- end ---`)
      resolve(1)
    }

    child.on("error", (error) => finish({ kind: "error", reason: `spawn error: ${error.message}` }))
    child.on("exit", () => finish(classifyOutput(output), "CLI exited before the Beacon sidebar panel appeared"))
    poll = setInterval(() => {
      const result = classifyOutput(output)
      if (result.kind !== "pending") finish(result)
    }, pollMs)
    timer = setTimeout(() => {
      finish(classifyOutput(output), `Beacon sidebar panel did not appear within ${timeoutMs}ms`)
    }, timeoutMs)
  })
}

function fakeChild() {
  const handlers = Object.create(null)
  return {
    pid: 4242,
    stdout: { on: (event, fn) => { handlers[`out:${event}`] = fn } },
    stderr: { on: (event, fn) => { handlers[`err:${event}`] = fn } },
    on: (event, fn) => { handlers[event] = fn },
    write: (text) => handlers["out:data"]?.(Buffer.from(text)),
    exit: () => handlers.exit?.(),
  }
}

function scriptedSpawn(output = "", delayMs = 5) {
  return () => {
    const child = fakeChild()
    if (output) setTimeout(() => child.write(output), delayMs)
    return child
  }
}

if (!process.argv.includes("--live")) {
  test("classifyOutput sees the ready panel", () => {
    assert.deepEqual(
      classifyOutput("sidebar\r\nBeacon  R0 C0"),
      { kind: "ready", reason: "Beacon sidebar panel visible" },
    )
  })

  test("classifyOutput lets each load error win over the ready marker", () => {
    for (const marker of FAILURE_MARKERS) {
      const result = classifyOutput(`Beacon\n${marker}`)
      assert.equal(result.kind, "error", marker)
      assert.equal(result.marker, marker)
    }
  })

  test("classifyOutput stays pending until a marker appears", () => {
    assert.equal(classifyOutput("booting").kind, "pending")
    assert.equal(classifyOutput("").kind, "pending")
  })

  test("readCliVersion reads the exact pinned CLI version", () => {
    assert.equal(readCliVersion(() => '{"cliVersion":"2.4.2","peers":{}}'), "2.4.2")
  })

  test("readCliVersion rejects a range and a missing version", () => {
    assert.throws(() => readCliVersion(() => '{"cliVersion":"^2.4.0"}'), /exact cliVersion/)
    assert.throws(() => readCliVersion(() => "{}"), /exact cliVersion/)
  })

  test("findPtyHelper skips when script is absent", () => {
    assert.equal(findPtyHelper(() => false), null)
    assert.equal(findPtyHelper((command) => command === "script"), "script")
  })

  test("buildRunCommand boots opencode standalone under script", () => {
    const { command, args } = buildRunCommand({ helper: "script" })
    assert.equal(command, "script")
    assert.ok(args.some((arg) => arg.includes("opencode --standalone")))
    assert.throws(() => buildRunCommand({ helper: "python" }), /unsupported PTY helper/)
  })

  test("runLive skips with an explicit log when no PTY helper exists", async () => {
    const logs = []
    const code = await runLive({ commandExists: () => false, log: (line) => logs.push(line) })
    assert.equal(code, 0)
    assert.match(logs.join("\n"), /SKIP: no pseudo-terminal helper/)
  })

  test("runLive fails when the installed CLI is not the pinned version", async () => {
    const logs = []
    const code = await runLive({
      commandExists: (command) => command === "script",
      readInstalledVersion: () => "opencode v9.9.9",
      log: (line) => logs.push(line),
    })
    assert.equal(code, 1)
    assert.match(logs.join("\n"), /expected 2\.4\.2/)
  })

  test("runLive passes when the Beacon panel appears", async () => {
    const logs = []
    const code = await runLive({
      spawnFn: scriptedSpawn("sidebar Beacon header"),
      commandExists: (command) => command === "script",
      kill: () => {},
      readInstalledVersion: () => "opencode v2.4.2",
      timeoutMs: 500,
      pollMs: 5,
      log: (line) => logs.push(line),
    })
    assert.equal(code, 0)
    assert.match(logs.join("\n"), /PASS/)
  })

  test("runLive fails on a load error even when the panel is present", async () => {
    const logs = []
    const code = await runLive({
      spawnFn: scriptedSpawn("Beacon\nNo renderer found"),
      commandExists: (command) => command === "script",
      kill: () => {},
      readInstalledVersion: () => "opencode v2.4.2",
      timeoutMs: 500,
      pollMs: 5,
      log: (line) => logs.push(line),
    })
    assert.equal(code, 1)
    assert.match(logs.join("\n"), /No renderer found/)
  })

  test("runLive fails on a bounded timeout when nothing renders", async () => {
    const logs = []
    const code = await runLive({
      spawnFn: scriptedSpawn(),
      commandExists: (command) => command === "script",
      kill: () => {},
      readInstalledVersion: () => "opencode v2.4.2",
      timeoutMs: 40,
      pollMs: 5,
      log: (line) => logs.push(line),
    })
    assert.equal(code, 1)
    assert.match(logs.join("\n"), /did not appear within 40ms/)
  })
} else {
  runLive().then((code) => process.exit(code))
}
