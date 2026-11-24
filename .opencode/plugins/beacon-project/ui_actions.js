// Control and dispatch actions from the panel. Every action reports its result
// through ctx.ui.toast.show and then refreshes the snapshot. The RPC/CLI
// transport is injected so the same code path is testable in plain Node.
import { errorMessage, str } from "./ui_model.js"

export function createActionRunner(ctx, source, { refresh = async () => {}, toast, confirm } = {}) {
  const show = toast ?? ((message, variant = "info") => ctx.ui?.toast?.show?.({ message, variant }))
  const ask = confirm ?? (async (run) => {
    if (typeof ctx.ui?.dialog?.confirm !== "function") return undefined
    return ctx.ui.dialog.confirm({
      title: "Cancel run",
      message: `Cancel run ${run.id}? This cannot be undone.`,
      label: { confirm: "Cancel run", cancel: "Keep run" },
    })
  })
  const copyID = (run) => {
    const text = [str(run.id), str(run.branch)].filter(Boolean).join(" · ")
    const method = ctx.renderer?.copyToClipboardOSC52
    if (typeof method !== "function") {
      show("Clipboard unavailable in this TUI", "warning")
      return false
    }
    let copied = false
    try { copied = method.call(ctx.renderer, text) === true } catch { copied = false }
    show(copied ? `Copied ${text}` : "Clipboard copy was refused", copied ? "success" : "warning")
    return copied
  }
  const control = async (action, run) => {
    try {
      const result = await source.control({ action, runID: str(run.id), sha: str(run.sha) || undefined })
      if (result?.ok === false) throw new Error(str(result.error || result.reason) || `${action} was refused`)
      show(`${action} accepted for ${str(run.id)}`, "success")
      return result
    } catch (error) {
      show(`${action} failed: ${errorMessage(error)}`, "error")
      return null
    } finally { await refresh() }
  }
  const dispatch = async () => {
    try {
      const result = await source.dispatch({ n: 1 })
      const count = Number(result?.started ?? result?.dispatched ?? result?.runs?.length ?? result?.startedRuns ?? 0)
      show(Number.isFinite(count) && count > 0
        ? `Dispatched ${count} task${count === 1 ? "" : "s"}` : "Nothing to dispatch right now", "success")
      return result
    } catch (error) {
      show(`Dispatch failed: ${errorMessage(error)}`, "error")
      return null
    } finally { await refresh() }
  }
  return async function runAction(name, run = null, opts = {}) {
    if (name === "dispatch") return dispatch()
    if (!run) return null
    if (name === "copy") return copyID(run)
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
    if (["retry", "approve", "cancel"].includes(name)) return control(name, run)
    return null
  }
}
