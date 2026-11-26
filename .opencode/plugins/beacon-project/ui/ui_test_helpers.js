// Shared fixtures for the Beacon TUI plugin tests.
export * from "./ui.js"
import { renderSnapshotLines } from "./ui.js"

export const NOW = Date.parse("2026-11-23T10:10:00Z")

export function makeRun(overrides = {}) {
  return {
    ID: "run-1",
    TaskID: "T-1",
    State: "running",
    PRNumber: 42,
    HeadSHA: "abcdef1234567890",
    Branch: "beacon/T-1",
    RebaseAttempts: 2,
    FixAttempts: 1,
    MaxAttempts: 3,
    Reason: "waiting CI",
    CreatedAt: "2026-11-23T10:00:00Z",
    UpdatedAt: "2026-11-23T10:07:00Z",
    Timeline: [{ at: "2026-11-23T10:05:00Z", kind: "checks_pending", state: "waiting_ci" }],
    Decisions: [{ at: "2026-11-23T10:06:00Z", kind: "reconcile.decision", verdict: "merge" }],
    Durations: { waiting_ci_seconds: 60, running_seconds: 5, blocked_seconds: 0 },
    ...overrides,
  }
}
export function solidModule(created = []) {
  return {
    createElement: (tag) => {
      const node = { tag, props: {}, children: [] }
      created.push(node)
      return node
    },
    insert: (parent, child) => { parent.children.push(child) },
    setProp: (node, name, value) => { node.props[name] = value },
  }
}

// Mirrors the real @opentui/solid host: nodes carry props, but insert() does
// not expose a JavaScript `children` array on the parent. Stubs built with
// solidModule() hide code that wrongly reads box.children; this one does not.
export function hostModule(created = []) {
  return {
    createElement: (tag) => {
      const node = { tag, props: {} }
      created.push(node)
      return node
    },
    insert: () => {},
    setProp: (node, name, value) => { node.props[name] = value },
  }
}

// Share the teardown wrapper between plugin setup and the status panel. A
// failing disposer is logged and skipped so the rest of cleanup still runs.
// The returned wrapper handles both sync and promise-returning disposers.
export function cleanupWrapper(prefix) {
  return (label, dispose) => {
    const failed = (error) => console.debug(`${prefix}${label} cleanup failed: ${error?.message ?? error}`)
    try {
      const result = dispose?.()
      if (result && typeof result.then === "function") return result.catch(failed)
    } catch (error) {
      failed(error)
    }
  }
}

// Host adapters are optional in test doubles. These helpers keep the fallback
// path identical for every mount and action runner.
export function propSetter(source) {
  return source?.setProp ?? ((node, key, value) => { node.props[key] = value })
}

export function toastShow(ctx, toast) {
  return toast ?? ((message, variant = "info") => ctx.ui?.toast?.show?.({ message, variant }))
}

export function themeStub() {
  return {
    text: {
      base: "base", muted: "muted", action: { primary: { base: "action" } },
      feedback: {
        error: { base: "error" }, warning: { base: "warning" },
        success: { base: "success" }, info: { base: "info" },
      },
    },
    background: { raised: { base: "panel", high: "hover" } },
  }
}

export function signalStub() {
  return { createSignal: (initial) => [() => initial, () => {}] }
}

export const lines = (snapshot, error = null, now = NOW) => renderSnapshotLines(snapshot, error, now)
