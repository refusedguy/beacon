// Run: node --test .opencode/plugins/beacon-project/protocol/cleanup_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import { cleanupWrapper } from "./cleanup.js"

function captureDebug(run) {
  const logs = []
  const original = console.debug
  console.debug = (line) => { logs.push(line) }
  try {
    return { result: run(), logs }
  } finally {
    console.debug = original
  }
}

async function captureDebugAsync(run) {
  const logs = []
  const original = console.debug
  console.debug = (line) => { logs.push(line) }
  try {
    const result = await run()
    return { result, logs }
  } finally {
    console.debug = original
  }
}

test("cleanupWrapper runs sync disposers and returns undefined", () => {
  const calls = []
  const cleanup = cleanupWrapper("test: ")
  const result = cleanup("one", () => calls.push("one"))
  assert.equal(result, undefined)
  assert.deepEqual(calls, ["one"])
})

test("cleanupWrapper tolerates missing disposers", () => {
  const cleanup = cleanupWrapper("test: ")
  assert.equal(cleanup("none"), undefined)
  assert.equal(cleanup("null", null), undefined)
})

test("cleanupWrapper logs and swallows sync errors", () => {
  const { result, logs } = captureDebug(() =>
    cleanupWrapper("test: ")("bad", () => { throw new Error("boom") }))
  assert.equal(result, undefined)
  assert.deepEqual(logs, ["test: bad cleanup failed: boom"])
})

test("cleanupWrapper catches and logs rejected disposers", async () => {
  const { result, logs } = await captureDebugAsync(async () =>
    cleanupWrapper("test: ")("async", async () => { throw new Error("later") }))
  assert.equal(result, undefined)
  assert.deepEqual(logs, ["test: async cleanup failed: later"])
})

test("the cleanup module has no TUI peer import", async () => {
  const src = await readFile(new URL("./cleanup.js", import.meta.url), "utf8")
  for (const needle of ["solid-js", "@opentui", "ui_runtime.js", "../ui/"]) {
    assert.equal(src.includes(needle), false, `cleanup.js must not reference ${needle}`)
  }
})

test("both call sites use the shared cleanup wrapper", async () => {
  const [server, helpers] = await Promise.all([
    readFile(new URL("../index.js", import.meta.url), "utf8"),
    readFile(new URL("../ui/ui_test_helpers.js", import.meta.url), "utf8"),
  ])
  assert.match(server, /from "\.\/protocol\/cleanup\.js"/)
  assert.match(helpers, /from "\.\.\/protocol\/cleanup\.js"/)
  for (const [name, src] of [["index.js", server], ["ui_test_helpers.js", helpers]]) {
    assert.equal(src.includes("function cleanupWrapper"), false, `${name} must not define its own wrapper`)
  }
})

test("both halves export the same teardown wrapper", async () => {
  const [server, helpers, shared] = await Promise.all([
    import("../index.js"),
    import("../ui/ui_test_helpers.js"),
    import("./cleanup.js"),
  ])
  assert.equal(server.cleanupWrapper, shared.cleanupWrapper)
  assert.equal(helpers.cleanupWrapper, shared.cleanupWrapper)
})
