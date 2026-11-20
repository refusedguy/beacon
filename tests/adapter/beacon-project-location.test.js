// Адаптер-тест T3.176: каркас V2-плагина и граница проекта.
// Запуск: node --test tests/adapter/beacon-project-location.test.js
//
// Источники: карточка T3.176, ADR 0069 (решения 1 и 2), документация OpenCode
// "Building plugins" и "Server plugin API / Context". Тест читает исходники
// плагина через node:fs и отдельно проверяет чистые функции location.js.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { isWorktree, locationInfo, projectRoot } from "../../.opencode/plugins/beacon-project/location.js"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const PLUGIN_DIR = join(REPO_ROOT, ".opencode", "plugins", "beacon-project")

const indexSource = readFileSync(join(PLUGIN_DIR, "index.js"), "utf8")
const locationSource = readFileSync(join(PLUGIN_DIR, "location.js"), "utf8")
const packageJSON = JSON.parse(readFileSync(join(PLUGIN_DIR, "package.json"), "utf8"))

test("T3.176: локальный пакет с серверной частью и Plugin.define", () => {
  assert.equal(packageJSON.type, "module")
  assert.equal(packageJSON.exports?.["."], "./index.js")
  assert.equal(packageJSON.dependencies?.["@opencode/plugin"], "latest")
  assert.match(indexSource, /import\s*\{\s*Plugin\s*\}\s*from\s*"@opencode\/plugin"/)
  assert.match(indexSource, /Plugin\.define\(\{/)
  assert.match(indexSource, /id:\s*"beacon-project"/)
  assert.match(indexSource, /export default beaconProject/)
  assert.match(indexSource, /async setup\(ctx\)/)
})

test("T3.176: корень берётся из ctx.location.project.canonical, без жёсткого cwd", () => {
  assert.match(indexSource, /projectRoot\(ctx\)/)
  assert.match(locationSource, /project\.canonical/)
  const hardcoded = ["\"cwd\": \"/", "cwd: \"/", "/mnt", "/projects"]
  for (const needle of hardcoded) {
    assert.equal(indexSource.includes(needle), false, needle)
    assert.equal(locationSource.includes(needle), false, needle)
  }
  assert.doesNotMatch(indexSource, /os\.Getwd|cwd\s*:\s*process\.cwd\(\)/)
})

test("T3.176: в worktree root равен canonical, а не directory", () => {
  const canonical = "/srv/beacon-main"
  const directory = "/srv/beacon-main/.worktree/w316"
  const ctx = {
    location: {
      directory,
      project: { id: "p_1", directory: canonical, canonical },
    },
  }
  const info = locationInfo(ctx)
  assert.equal(info.canonical, canonical)
  assert.equal(info.directory, directory)
  assert.equal(info.projectID, "p_1")
  assert.equal(info.root, canonical)
  assert.equal(projectRoot(ctx), canonical)
  assert.equal(isWorktree(ctx), true)
})

test("T3.176: вне worktree root и directory совпадают", () => {
  const root = "/srv/beacon-main"
  const ctx = {
    location: {
      directory: root,
      project: { id: "p_1", directory: root, canonical: root },
    },
  }
  assert.equal(projectRoot(ctx), root)
  assert.equal(isWorktree(ctx), false)
})

test("T3.176: без ctx.location плагин ничего не меняет (fail-open)", () => {
  assert.equal(locationInfo({}), null)
  assert.equal(projectRoot({}), null)
  assert.equal(isWorktree({}), false)
})
