import { WORKER_ROLE, agentIdentity, agentRole, guardedAgents } from "./state/session_identity.js"

export { guardedAgents }

// Advisory guard at the tool boundary. It catches the command shapes the
// project documents for workers, and it can be relaxed with
// BEACON_SHELL_GUARD=warn. This is not a security boundary: quoting, aliases
// and scripts can hide a command. Keep each rule narrow so ordinary work, such
// as pushing a branch named feature/main-fix, is never blocked.
export const SHELL_GUARD_RULES = Object.freeze([
  {
    id: "recursive grep",
    pattern: /\bgrep\b[^\n]*\s-(?:-recursive\b|[A-Za-z]*[rR][A-Za-z]*\b)/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "root find",
    pattern: /\bfind\b\s+\/+(?=\s|$)/,
    replacement: "rg -n PATTERN <explicit-directory>",
  },
  {
    id: "direct push to main",
    pattern: /\bgit\s+push\b[^\n]*(?:^|[\s"':])(?:main|master)(?=$|[\s"':])/,
    replacement: "push to your own branch and open a pull request",
  },
  {
    id: "full go build",
    pattern: /\bgo\s+build\b[^\n]*\.\/\.\.\./,
    replacement: "a targeted go build -o <path> <package>, or leave the full build to CI",
  },
  {
    id: "full go test suite",
    pattern: /\bgo\s+test\b[^\n]*\.\/\.\.\./,
    replacement: "go test for the changed packages, or leave the full suite to CI",
  },
  {
    id: "full go vet",
    pattern: /\bgo\s+vet\b[^\n]*\.\/\.\.\./,
    replacement: "go vet on the changed packages, or leave the full vet to CI",
  },
  {
    id: "full lint run",
    pattern: /\bgolangci-lint\s+run\b/,
    replacement: "the linter in CI, or lint only the changed packages",
  },
  {
    id: "npm ci",
    pattern: /\bnpm\s+ci\b/,
    replacement: "npm ci in CI, or install only the packages you need",
  },
  {
    id: "npm run build",
    pattern: /\bnpm\s+run\s+build\b/,
    replacement: "npm run build in CI, or build a single package",
  },
])

const GUARDED_TOOLS = new Set(["bash", "shell", "execute"])

export function commandFromToolInput(input) {
  if (typeof input === "string") return input
  if (!input || typeof input !== "object") return ""
  for (const key of ["command", "cmd", "code", "script"]) {
    if (typeof input[key] === "string") return input[key]
  }
  const args = input.args ?? input.arguments
  if (typeof args === "string") return args
  if (Array.isArray(args)) return args.filter((part) => typeof part === "string").join(" ")
  return ""
}

const SHELL_SEPARATORS = /[;&|\n]+/
const ASSIGNMENT_PREFIX = /^(?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s]*)?\s+)/
const ENV_PREFIX = /^env(?:\s+[A-Za-z_][A-Za-z0-9_]*=[^\s]+)*\s+/
const SUDO_PREFIX = /^sudo(?:\s+-(?:u|g)(?:[=\s]\S+)?|\s+-[A-Za-z0-9]+)*\s+/
const BACKTICK_SUBSTITUTION = /`([^`]*)`/g

// A command often hides the interesting call behind assignments, `env` or
// `sudo`. Remove those wrappers repeatedly so nested forms such as
// `PATH=/bin sudo -u admin find /` reduce to the command being run.
function stripCommandPrefix(part) {
  let text = typeof part === "string" ? part.trim() : ""
  for (;;) {
    const before = text
    const assignment = text.match(ASSIGNMENT_PREFIX)
    if (assignment) text = text.slice(assignment[0].length)
    const env = text.match(ENV_PREFIX)
    if (env) text = text.slice(env[0].length)
    const sudo = text.match(SUDO_PREFIX)
    if (sudo) text = text.slice(sudo[0].length)
    if (text === before) break
  }
  return text
}

// Both substitution forms are walked with balanced parentheses for $(...) so
// a nested $(...) does not cut the outer command short.
export function commandSubstitutions(command) {
  const found = []
  if (typeof command !== "string") return found
  for (let i = 0; i < command.length; i++) {
    if (command[i] !== "$" || command[i + 1] !== "(") continue
    let depth = 1
    let end = i + 2
    for (; end < command.length && depth > 0; end++) {
      if (command[end] === "(") depth++
      else if (command[end] === ")") depth--
    }
    if (depth === 0) found.push(command.slice(i + 2, end - 1))
  }
  BACKTICK_SUBSTITUTION.lastIndex = 0
  let match
  while ((match = BACKTICK_SUBSTITUTION.exec(command)) !== null) found.push(match[1])
  return found
}

// Reduce one tool input to the individual commands a shell would run. Split on
// operators, strip prefixes, then recurse into substitutions. The depth cap
// keeps a pathological input from looping forever.
export function commandSegments(command, depth = 0) {
  if (typeof command !== "string" || !command || depth > 8) return []
  const segments = []
  for (const raw of command.split(SHELL_SEPARATORS)) {
    const stripped = stripCommandPrefix(raw)
    if (stripped) segments.push(stripped)
  }
  for (const inner of commandSubstitutions(command)) {
    segments.push(...commandSegments(inner, depth + 1))
  }
  return segments
}

function compileShellGuardRule(entry, log = console.warn) {
  if (!entry || typeof entry !== "object") return null
  const { id, replacement } = entry
  if (typeof id !== "string" || !id || typeof replacement !== "string") return null
  let pattern
  try {
    if (entry.pattern instanceof RegExp) {
      const flags = entry.pattern.flags.replace(/[gy]/g, "")
      pattern = new RegExp(entry.pattern.source, flags)
    } else if (typeof entry.pattern === "string") {
      pattern = new RegExp(entry.pattern)
    } else {
      return null
    }
  } catch (error) {
    log(`beacon shell guard: ignoring rule "${id}" with invalid pattern: ${error.message}`)
    return null
  }
  return Object.freeze({ id, pattern, replacement })
}

export function buildShellGuardRules(config = {}, log = console.warn) {
  const configured = config.shellGuardRules
  if (!Array.isArray(configured)) return SHELL_GUARD_RULES
  const additions = configured
    .map((entry) => compileShellGuardRule(entry, log))
    .filter(Boolean)
  if (!additions.length) return SHELL_GUARD_RULES
  return Object.freeze([...SHELL_GUARD_RULES, ...additions])
}

export function findShellGuardHit(tool, input, rules = SHELL_GUARD_RULES) {
  if (!GUARDED_TOOLS.has(tool)) return null
  const command = commandFromToolInput(input)
  if (!command) return null
  for (const segment of commandSegments(command)) {
    for (const rule of rules) {
      rule.pattern.lastIndex = 0
      if (rule.pattern.test(segment)) return rule
    }
  }
  return null
}

function guardAppliesToAgent(event, config = {}) {
  const agent = agentIdentity(null, event)
  // When the event carries no agent, keep the guard as a safety net.
  if (!agent) return true
  return agentRole(agent, config) === WORKER_ROLE
}

export function guardMode(config = {}, env = process.env) {
  const fromEnv = env.BEACON_SHELL_GUARD
  if (typeof fromEnv === "string") return fromEnv
  return config.shellGuard
}

// Workers inherit the pooled MCP daemon from the parent session. The shared
// agentRole classifier answers this, so MCP, the shell guard and the rules
// agree on who is a worker. Names such as "networker" never match by substring.
export function isWorkerAgent(agent, config = {}) {
  return agentRole(agent, config) === WORKER_ROLE
}

export async function registerShellGuard(ctx, config = {}, env = process.env) {
  if (typeof ctx?.tool?.hook !== "function") return null
  const mode = guardMode(config, env)
  const rules = buildShellGuardRules(config)
  try {
    return await ctx.tool.hook("execute.before", (event) => {
      if (!guardAppliesToAgent(event, config)) return
      const hit = findShellGuardHit(event?.tool, event?.input, rules)
      if (!hit) return
      const message = `${hit.id} is blocked here. Use ${hit.replacement}.`
      console.warn(message)
      if (mode === "warn") return
      throw new Error(message)
    })
  } catch {
    return null
  }
}
