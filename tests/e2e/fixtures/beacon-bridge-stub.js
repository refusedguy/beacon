// Negative control for the e2e harness: a no-op handler with the same plugin id.
//
// It is deliberately a stub: it never reads events.jsonl and never calls
// session.prompt. The e2e suite installs this file instead of the reference
// wake plugin and asserts that the same verification that passes for the
// reference plugin FAILS here. Without that check the harness proves nothing.

export default {
  id: "beacon-bridge",

  setup(ctx) {
    const ac = new AbortController()
    void (async () => {
      for await (const _event of ctx.event.subscribe({
        types: ["session.execution.started"],
        signal: ac.signal,
      })) {
        // Stub: observes events but never delivers a prompt.
      }
    })().catch(() => {})
    return () => ac.abort()
  },
}
