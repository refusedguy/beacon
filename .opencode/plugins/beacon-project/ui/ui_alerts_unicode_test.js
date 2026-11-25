// Non-ASCII coverage for end-clipped display text.
import { test } from "node:test"
import assert from "node:assert/strict"
import { truncate } from "./ui_alerts.js"

test("truncate clips by code point, not UTF-16 unit", () => {
  assert.equal(truncate("日本語テスト", 4), "日本語…")
  assert.equal(truncate("a😀b😀c", 4), "a😀b…")
})

test("truncate keeps multibyte text that fits the width", () => {
  assert.equal(truncate("😀😀😀", 4), "😀😀😀")
  assert.equal(truncate("日本語", 10), "日本語")
})
