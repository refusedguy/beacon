// One FNV-1a hash for request ids. The plugin server and the TUI both need the
// same id for one body, so this module imports no TUI peer. The body stays out
// of the log; only its hash is printed.
export const bodyHash = (input) => {
  const text = JSON.stringify(input ?? null)
  let hash = 0x811c9dc5
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i)
    hash = Math.imul(hash, 0x01000193)
  }
  return (hash >>> 0).toString(16).padStart(8, "0")
}

export const requestId = (method, input) => `${method}:${bodyHash(input)}`
export const dispatchRequestId = (input) => requestId("dispatch", input)
