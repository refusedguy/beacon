// Run: node --test .opencode/plugins/beacon-project/protocol/body_hash_test.js
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import { bodyHash, dispatchRequestId, requestId } from "./body_hash.js"
import { bodyHash as serverBodyHash, requestId as serverRequestId } from "../ui/ui_model.js"

const bodies = [null, undefined, {}, { n: 1 }, { n: "3" }, "abc", [1, 2]]

test("bodyHash is stable, input-specific and zero padded", () => {
  assert.equal(bodyHash({ n: 1 }), bodyHash({ n: 1 }))
  assert.notEqual(bodyHash({ n: 1 }), bodyHash({ n: 2 }))
  for (const body of bodies) assert.match(bodyHash(body), /^[0-9a-f]{8}$/)
  assert.equal(bodyHash(null), "77074ba4")
  assert.equal(bodyHash({ n: 1 }), "fdef5c20")
  assert.equal(bodyHash({ n: 3 }), "01e9523e")
})

test("request ids are method prefixes over the shared hash", () => {
  for (const body of bodies) {
    assert.equal(requestId("dispatch", body), `dispatch:${bodyHash(body)}`)
    assert.equal(dispatchRequestId(body), requestId("dispatch", body))
    assert.equal(dispatchRequestId(body), serverRequestId("dispatch", body))
    assert.equal(bodyHash(body), serverBodyHash(body))
  }
})

test("the hash module has no TUI peer import", async () => {
  const src = await readFile(new URL("./body_hash.js", import.meta.url), "utf8")
  for (const needle of ["solid-js", "@opentui", "ui_runtime.js", "../ui/"]) {
    assert.equal(src.includes(needle), false, `body_hash.js must not reference ${needle}`)
  }
})

test("both call sites use the shared hash", async () => {
  const [server, tui] = await Promise.all([
    readFile(new URL("../index.js", import.meta.url), "utf8"),
    readFile(new URL("../ui/ui_actions.js", import.meta.url), "utf8"),
  ])
  assert.match(server, /from "\.\/protocol\/body_hash\.js"/)
  assert.match(tui, /from "\.\.\/protocol\/body_hash\.js"/)
  for (const [name, src] of [["index.js", server], ["ui_actions.js", tui]]) {
    assert.equal(src.includes("0x811c9dc5"), false, `${name} must not repeat the FNV seed`)
    assert.equal(src.includes("0x01000193"), false, `${name} must not repeat the FNV prime`)
  }
})
