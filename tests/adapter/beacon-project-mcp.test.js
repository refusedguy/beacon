// Адаптер-тест T3.177: MCP-сервер Beacon через ctx.mcp.transform.
// Запуск: node --test tests/adapter/beacon-project-mcp.test.js
//
// Источники: карточка T3.177, ADR 0069 (решения 3 и 5), документация OpenCode
// "Server plugin API / MCP" и "MCP servers / local options". Go-часть проверяется
// исходниками здесь, а поведение stateDir — go test ./cmd/beacon.

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import {
  beaconServerConfig,
  BEACON_PROJECT_ROOT_ENV,
  BEACON_SERVER,
  registerBeaconMcp,
} from "../../.opencode/plugins/beacon-project/mcp.js"

const SELF = fileURLToPath(import.meta.url)
const REPO_ROOT = resolve(dirname(SELF), "..", "..")
const PLUGIN_DIR = join(REPO_ROOT, ".opencode", "plugins", "beacon-project")

const mcpSource = readFileSync(join(PLUGIN_DIR, "mcp.js"), "utf8")
const indexSource = readFileSync(join(PLUGIN_DIR, "index.js"), "utf8")
const doctorSource = readFileSync(join(REPO_ROOT, "cmd", "beacon", "doctor.go"), "utf8")

function editorHarness() {
  const calls = []
  const servers = new Map()
  const editor = {
    list() {
      return [...servers]
    },
    get(name) {
      return servers.get(name)
    },
    set(name, config) {
      calls.push(["set", name, config])
      servers.set(name, { ...config })
    },
    update(name, update) {
      calls.push(["update", name])
      const config = servers.get(name)
      if (config) update(config)
    },
    remove(name) {
      calls.push(["remove", name])
      servers.delete(name)
    },
  }
  return { calls, editor, servers }
}

test("T3.177: конфиг сервера выводится из корня, без фиксированного cwd", () => {
  const root = "/srv/canonical-project"
  const config = beaconServerConfig(root)
  assert.equal(config.type, "local")
  assert.equal(config.cwd, root)
  assert.equal(config.environment[BEACON_PROJECT_ROOT_ENV], root)
  assert.equal(config.disabled, false)
  assert.equal(Array.isArray(config.command), true)
  assert.equal(config.command[0].startsWith("/"), false)
  assert.match(mcpSource, /ctx\.mcp\.transform/)
  assert.match(mcpSource, /ctx\.mcp\.reload/)
  assert.match(mcpSource, /editor\.set/)
  assert.match(mcpSource, /editor\.update/)
  assert.match(mcpSource, /disabled\s*=\s*false/)
  for (const needle of ["/mnt", "/projects", "cwd: \"/"]) {
    assert.equal(mcpSource.includes(needle), false, needle)
  }
})

test("T3.177: канонический корень из location попадает в MCP-регистрацию", () => {
  assert.match(indexSource, /registerBeaconMcp\(ctx, root\)/)
  assert.match(indexSource, /projectRoot\(ctx\)/)
  assert.throws(() => beaconServerConfig(""), /корень проекта/)
})

test("T3.177: transform задаёт сервер и reload вызывается при смене корня", async () => {
  const { calls, editor, servers } = editorHarness()
  let callback = null
  let reloads = 0
  const ctx = {
    mcp: {
      async transform(fn) {
        callback = fn
        return { dispose: async () => {} }
      },
      async reload() {
        reloads++
        callback(editor)
      },
    },
  }

  const registration = await registerBeaconMcp(ctx, "/srv/project-a")
  assert.equal(typeof callback, "function")
  callback(editor)
  assert.equal(servers.get(BEACON_SERVER).cwd, "/srv/project-a")
  assert.equal(servers.get(BEACON_SERVER).disabled, false)
  assert.equal(calls.some((call) => call[0] === "set"), true)

  await registration.setRoot("/srv/project-b")
  assert.equal(reloads, 1)
  assert.equal(servers.get(BEACON_SERVER).cwd, "/srv/project-b")
  assert.equal(servers.get(BEACON_SERVER).environment[BEACON_PROJECT_ROOT_ENV], "/srv/project-b")
  assert.equal(servers.get(BEACON_SERVER).disabled, false)

  await registration.setRoot("/srv/project-b")
  assert.equal(reloads, 1, "неизменённый корень не должен вызывать reload")
})

test("T3.177: Go получает корень явно, он главнее os.Getwd", () => {
  assert.match(doctorSource, /const beaconProjectRootEnv = "BEACON_PROJECT_ROOT"/)
  assert.match(doctorSource, /func stateDir\(root \.\.\.string\)/)
  assert.match(doctorSource, /os\.Getenv\(beaconProjectRootEnv\)/)
  assert.match(doctorSource, /beaconStateDirEnv/)
  // Явный корень разбирается до вызова os.Getwd().
  const explicit = doctorSource.indexOf("projectRoot != \"\"")
  const cwd = doctorSource.indexOf("cwd, err := os.Getwd()")
  assert.ok(explicit > 0 && cwd > explicit, "explicit root must be handled before os.Getwd()")
})
