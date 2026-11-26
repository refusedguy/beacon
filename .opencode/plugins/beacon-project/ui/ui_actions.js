// Control and dispatch actions from the panel. Every action reports its result
// through ctx.ui.toast.show and then refreshes the snapshot. The RPC/CLI
// transport is injected so the same code path is testable in plain Node.
import { CONTROL_ACTIONS, errorMessage, str } from "./ui_model.js"
import { isDispatching, setDispatching } from "./ui_alerts.js"
import { refreshDispatchingViews } from "./ui_sidebar.js"
import { toastShow } from "./ui_test_helpers.js"

// A mouse gesture on the Dispatch row reaches both onMouseDown and onMouseUp.
// Equal bodies inside this window come from one gesture, so they share one
// request and one POST. The hash makes the duplicate visible in the log.
export const DISPATCH_DEDUPE_MS = 250
export function dispatchRequestId(input) {
  const text = JSON.stringify(input ?? null)
  let hash = 0x811c9dc5
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193)
  }
  return `dispatch:${(hash >>> 0).toString(16).padStart(8, "0")}`
}

// Shared OSC52 copier. The toast reports the clipboard result, never a guess.
export function clipboardCopier(ctx, toast) {
  const show = toastShow(ctx, toast)
  return (text, label = text) => {
    if (!text) return false
    const method = ctx.renderer?.copyToClipboardOSC52
    if (typeof method !== "function") {
      show("Clipboard unavailable in this TUI", "warning")
      return false
    }
    let copied = false
    try { copied = method.call(ctx.renderer, text) === true } catch { copied = false }
    show(copied ? `Copied ${label}` : "Clipboard copy was refused", copied ? "success" : "warning")
    return copied
  }
}

export function createActionRunner(ctx, source, { refresh = async () => {}, toast, confirm, log = console.debug } = {}) {
  const show = toastShow(ctx, toast)
  const copyText = clipboardCopier(ctx, show)
  const ask = confirm ?? (async (run) => {
    if (typeof ctx.ui?.dialog?.confirm !== "function") return undefined
    return ctx.ui.dialog.confirm({
      title: "Cancel run",
      message: `Cancel run ${run.id}? This cannot be undone.`,
      label: { confirm: "Cancel run", cancel: "Keep run" },
    })
  })
  const copyID = (run) => copyText([str(run.id), str(run.branch)].filter(Boolean).join(" · "))
  const copyPR = (run) => {
    const url = str(run.prURL)
    if (!url) { show("No PR URL for this run", "warning"); return false }
    return copyText(url)
  }
  const control = async (action, run, message) => {
    try {
      const input = { action, runID: str(run.id), sha: str(run.sha) || undefined }
      if (message) input.message = str(message)
      const result = await source.control(input)
      if (result?.ok === false) throw new Error(str(result.error || result.reason) || `${action} was refused`)
      show(`${action} accepted for ${str(run.id)}`, "success")
      return result
    } catch (error) {
      show(`${action} failed: ${errorMessage(error)}`, "error")
      return null
    } finally { await refresh() }
  }
  const nudge = async (run) => {
    if (typeof ctx.ui?.dialog?.prompt !== "function") {
      show("Nudge unavailable in this TUI", "warning")
      await refresh()
      return null
    }
    const answer = await ctx.ui.dialog.prompt({
      title: "Nudge worker",
      description: "Send guidance to the running worker",
      placeholder: "...",
    })
    const message = str(answer).trim()
    if (!message) { show("No guidance sent", "info"); await refresh(); return null }
    return control("nudge", run, message)
  }
  let lastDispatch = null
  const dispatch = () => {
    const input = { n: 1 }
    const id = dispatchRequestId(input)
    const now = Date.now()
    const previous = lastDispatch
    if (previous && previous.id === id && now - previous.at < DISPATCH_DEDUPE_MS) {
      log(`beacon dispatch duplicate suppressed id=${id}`)
      return previous.promise
    }
    // The time window covers one mouse gesture. The in-flight flag also blocks
    // a later click that arrives while the first dispatch is still pending.
    if (isDispatching()) {
      log(`beacon dispatch already in flight id=${id}`)
      return previous?.promise ?? null
    }
    log(`beacon dispatch click id=${id}`)
    setDispatching(true)
    refreshDispatchingViews()
    const promise = (async () => {
      try {
        const result = await source.dispatch(input)
        const count = Number(result?.started ?? result?.dispatched ?? result?.runs?.length ?? result?.startedRuns ?? 0)
        show(Number.isFinite(count) && count > 0
          ? `Dispatched ${count} task${count === 1 ? "" : "s"}` : "Nothing to dispatch right now", "success")
        return result
      } catch (error) {
        show(`Dispatch failed: ${errorMessage(error)}`, "error")
        return null
      } finally {
        setDispatching(false)
        refreshDispatchingViews()
        await refresh()
      }
    })()
    lastDispatch = { id, at: now, promise }
    return promise
  }
  return async function runAction(name, run = null, opts = {}) {
    if (name === "dispatch") return dispatch()
    if (!run) return null
    if (name === "copy") return copyID(run)
    if (name === "pr") return copyPR(run)
    if (name === "help") return nudge(run)
    if (name === "cancel" && opts.confirmed !== true) {
      if (typeof ctx.ui?.dialog?.confirm !== "function") {
        show("Confirmation unavailable, run kept", "warning")
        await refresh()
        return null
      }
      const agreed = await ask(run)
      if (agreed !== true) {
        show("Run kept, nothing cancelled", "info")
        await refresh()
        return null
      }
    }
    if (CONTROL_ACTIONS.includes(name)) return control(name, run, opts.message)
    return null
  }
}
