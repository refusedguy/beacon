// Control and dispatch actions from the panel. Every action reports its result
// through ctx.ui.toast.show and then refreshes the snapshot. The RPC/CLI
// transport is injected so the same code path is testable in plain Node.
import { CONTROL_ACTIONS, errorMessage, str } from "./ui_model.js"
import { isDispatching, setDispatching } from "./ui_alerts.js"
import { refreshDispatchingViews } from "./ui_sidebar.js"
import { dispatchRequestId } from "../protocol/body_hash.js"
export { dispatchRequestId }

// Host adapters are optional in test doubles. This fallback keeps the toast
// path identical for every action runner without importing the test helper.
const toastShow = (ctx, toast) =>
  toast ?? ((message, variant = "info") => ctx.ui?.toast?.show?.({ message, variant }))

// A mouse gesture on the Dispatch row reaches both onMouseDown and onMouseUp.
// Equal bodies inside this window come from one gesture, so they share one
// request and one POST. The hash makes the duplicate visible in the log.
const DISPATCH_DEDUPE_MS = 250

// Shared OSC52 copier. The toast reports the clipboard result, never a guess.
export function clipboardCopier(ctx, toast) {
  const show = toastShow(ctx, toast)
  return (text, label = text) => {
    if (!text) return false
    const method = ctx.renderer?.copyToClipboardOSC52
    if (typeof method !== "function") {
      show("Clipboard unavailable in this TUI", "warning")
      show("Next: copy the ID from the run row", "info")
      return false
    }
    let copied = false
    try { copied = method.call(ctx.renderer, text) === true } catch { copied = false }
    show(copied ? `Copied ${label}` : "Clipboard copy was refused", copied ? "success" : "warning")
    return copied
  }
}

export function createActionRunner(ctx, source, { refresh = async () => {}, afterDispatch = async () => {}, toast, confirm, log = console.debug } = {}) {
  const show = toastShow(ctx, toast)
  const copyText = clipboardCopier(ctx, show)
  const ask = confirm ?? (async (run, action) => {
    if (typeof ctx.ui?.dialog?.confirm !== "function") return undefined
    const isCancel = action === "cancel"
    const title = isCancel ? "Cancel run" : "Retry run"
    return ctx.ui.dialog.confirm({
      title,
      message: isCancel ? `Cancel run ${run.id}? This cannot be undone.`
        : `Retry run ${run.id}? This starts a new attempt.`,
      label: { confirm: title, cancel: "Keep run" },
    })
  })
  const copyID = (run) => copyText([str(run.id), str(run.branch)].filter(Boolean).join(" · "))
  const copyPR = (run) => {
    const url = str(run.prURL)
    if (!url) {
      show("No PR URL for this run", "warning")
      show("Next: wait for the worker to open a PR", "info")
      return false
    }
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
      show(`${action} failed: ${errorMessage(error)} · check beacon status and retry`, "error")
      return null
    } finally { await refresh() }
  }
  const nudge = async (run) => {
    if (typeof ctx.ui?.dialog?.prompt !== "function") {
      show("Nudge unavailable in this TUI", "warning")
      show("Next: use beacon_control nudge from the CLI", "info")
      await refresh()
      return null
    }
    const answer = await ctx.ui.dialog.prompt({
      title: "Nudge worker",
      description: "Send guidance to the running worker",
      placeholder: "...",
    })
    const message = str(answer).trim()
    if (!message) {
      show("No guidance sent", "info")
      show("Next: enter guidance and try again", "info")
      await refresh()
      return null
    }
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
    let refreshed = Promise.resolve()
    const promise = (async () => {
      try {
        // Start dispatch first so the source can mark the follow-up read as
        // covered by it. That read reuses the returned run data instead of
        // starting a second CLI process, and the watcher can supersede it.
        const dispatched = source.dispatch(input)
        refreshed = refresh()
        const result = await dispatched
        const count = Number(result?.started ?? result?.dispatched ?? result?.runs?.length ?? result?.startedRuns ?? 0)
        show(Number.isFinite(count) && count > 0
          ? `Dispatched ${count} task${count === 1 ? "" : "s"}` : "Nothing to dispatch right now · add a ready task card", "success")
        return result
      } catch (error) {
        show(`Dispatch failed: ${errorMessage(error)}`, "error")
        return null
      } finally {
        setDispatching(false)
        refreshDispatchingViews()
        await refreshed
        await afterDispatch()
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
        show("Confirmation unavailable, run kept · use beacon_control from the CLI", "warning")
        await refresh()
        return null
      }
      if (await ask(run, "cancel") !== true) {
        show("Run kept, nothing cancelled", "info")
        await refresh()
        return null
      }
    }
    // A retry only asks when the host can show a dialog. Without one the
    // control still runs, so plain Node callers keep the same path as before.
    if (name === "retry" && opts.confirmed !== true && typeof ctx.ui?.dialog?.confirm === "function") {
      if (await ask(run, "retry") !== true) {
        show("Run kept, nothing retried", "info")
        await refresh()
        return null
      }
    }
    if (CONTROL_ACTIONS.includes(name)) return control(name, run, opts.message)
    return null
  }
}
