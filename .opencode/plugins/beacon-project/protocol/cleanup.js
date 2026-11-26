// One teardown wrapper for both plugin halves. The server plugin and the TUI
// status panel dispose their resources in sequence, so this module imports no
// TUI peer. A failing disposer is logged and skipped so cleanup continues.
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
