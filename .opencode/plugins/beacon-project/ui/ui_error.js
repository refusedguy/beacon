// A failed CLI poll puts Cobra usage, help flags and stack traces in stderr.
// Keep sidebar errors to one short line before they reach any signal.
const ANSI_ESCAPE = /\u001b\[[0-?]*[ -/]*[@-~]/g
const CLI_NOISE = /--help|usage:|unknown (?:command|flag)|^(?:[\w.]*Error|Error):|\bat\s+\S+:\d+(?::\d+)?|^\s*[\[{]|"error"\s*:/i
const FALLBACK = "beacon status temporarily unavailable"
const MAX_CHARS = 120

// Every stale banner carries this one command. The operator can check the
// daemon without opening the config file or guessing a flag.
export const STALE_ACTION = "retry: beacon status --snapshot"

export function cleanErrorMessage(cause) {
  const raw = typeof cause === "string" ? cause
    : cause && typeof cause.message === "string" ? cause.message : String(cause)
  const line = raw.replace(ANSI_ESCAPE, "").replace(/\s+/g, " ").trim().slice(0, MAX_CHARS)
  return line && !CLI_NOISE.test(line) ? line : FALLBACK
}
